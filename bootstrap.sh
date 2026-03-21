#!/usr/bin/env bash
set -euo pipefail

# =============================================================================
# cub-vmcluster bootstrap
# =============================================================================
# Interactive setup that discovers your AWS environment and generates
# configuration to run the vmcluster worker locally via Docker.
#
# Prerequisites:
#   - AWS CLI configured and authenticated
#   - cub CLI authenticated to ConfigHub
#
# Usage:
#   ./bootstrap.sh

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

log_info()    { echo -e "${BLUE}▸${NC} $*"; }
log_success() { echo -e "${GREEN}✓${NC} $*"; }
log_warn()    { echo -e "${YELLOW}⚠${NC} $*"; }
log_error()   { echo -e "${RED}✗${NC} $*" >&2; }
die()         { log_error "$@"; exit 1; }

prompt() {
    local var="$1" prompt="$2" default="${3:-}"
    if [[ -n "$default" ]]; then
        echo -en "${BOLD}${prompt}${NC} [${default}]: "
        read -r input
        eval "$var=\"${input:-$default}\""
    else
        echo -en "${BOLD}${prompt}${NC}: "
        read -r input
        eval "$var=\"$input\""
    fi
}

# Portable replacement for mapfile (bash 3.2 compatible)
# Usage: lines_to_array ARRAYNAME "$MULTILINE_STRING"
lines_to_array() {
    local _arrname="$1" _input="$2"
    local _i=0
    eval "$_arrname=()"
    while IFS= read -r _line; do
        [[ -n "$_line" ]] && eval "${_arrname}[${_i}]=\"\${_line}\"" && ((_i++))
    done <<< "$_input"
}

pick_from_list() {
    local var="$1" prompt="$2"
    shift 2
    local options=("$@")

    if [[ ${#options[@]} -eq 0 ]]; then
        return 1
    fi

    echo ""
    echo -e "${BOLD}${prompt}${NC}"
    local i=1
    for opt in "${options[@]}"; do
        echo "  $i) $opt"
        i=$((i + 1))
    done
    echo ""
    echo -en "  ${BOLD}Choose${NC} [1]: "
    read -r choice
    choice="${choice:-1}"

    if [[ "$choice" -lt 1 || "$choice" -gt ${#options[@]} ]] 2>/dev/null; then
        die "Invalid choice"
    fi

    eval "$var=\"${options[$((choice-1))]}\""
}

# =============================================================================
echo ""
echo -e "${BOLD}cub-vmcluster bootstrap${NC}"
echo "This will configure a local vmcluster worker to provision k3s clusters on AWS."
echo ""

# --- Check prerequisites ---
command -v aws >/dev/null 2>&1 || die "AWS CLI not found. Install it first."
command -v cub >/dev/null 2>&1 || die "cub CLI not found. Install it first: https://docs.confighub.com/cli"

# Verify AWS auth
log_info "Checking AWS credentials..."
AWS_ACCOUNT=$(aws sts get-caller-identity --query Account --output text 2>/dev/null) || \
    die "Not authenticated to AWS. Run 'aws configure' or 'aws sso login' first."
AWS_REGION=$(aws configure get region 2>/dev/null || echo "")
log_success "AWS account: $AWS_ACCOUNT"

# Detect AWS auth method for Docker
AWS_AUTH_METHOD=""
DOCKER_AWS_ARGS=""
if [[ -n "${AWS_ACCESS_KEY_ID:-}" ]]; then
    AWS_AUTH_METHOD="env"
    log_info "AWS auth: environment variables"
elif [[ -n "${AWS_PROFILE:-}" ]]; then
    AWS_AUTH_METHOD="profile"
    log_info "AWS auth: profile $AWS_PROFILE"
elif aws configure get sso_account_id >/dev/null 2>&1; then
    # Default profile uses SSO
    AWS_AUTH_METHOD="sso"
    log_info "AWS auth: SSO (default profile)"
else
    # Static credentials in default profile or instance profile
    AWS_AUTH_METHOD="default"
    log_info "AWS auth: default credentials"
fi

if [[ -z "$AWS_REGION" ]]; then
    prompt AWS_REGION "AWS region" "us-east-2"
fi
log_success "Region: $AWS_REGION"

# Verify cub auth
log_info "Checking ConfigHub credentials..."
CUB_SERVER=$(cub config get server 2>/dev/null || echo "")
if [[ -z "$CUB_SERVER" ]]; then
    die "Not authenticated to ConfigHub. Run 'cub auth login' first."
fi
log_success "ConfigHub: $CUB_SERVER"

# --- Discover VPCs ---
log_info "Discovering VPCs in $AWS_REGION..."
VPC_LIST=$(aws ec2 describe-vpcs --region "$AWS_REGION" \
    --query "Vpcs[].[VpcId,CidrBlock,Tags[?Key=='Name']|[0].Value || 'unnamed']" \
    --output text | while read -r id cidr name; do echo "$id ($name, $cidr)"; done)

if [[ -z "$VPC_LIST" ]]; then
    die "No VPCs found in $AWS_REGION. Create one first or choose a different region."
fi

lines_to_array VPC_OPTIONS "$VPC_LIST"
pick_from_list VPC_CHOICE "Select a VPC:" "${VPC_OPTIONS[@]}"
VPC_ID=$(echo "$VPC_CHOICE" | cut -d' ' -f1)
log_success "VPC: $VPC_ID"

# --- Discover subnets ---
log_info "Discovering public subnets in $VPC_ID..."
SUBNET_LIST=$(aws ec2 describe-subnets --region "$AWS_REGION" \
    --filters "Name=vpc-id,Values=$VPC_ID" \
    --query "Subnets[].[SubnetId,AvailabilityZone,CidrBlock,MapPublicIpOnLaunch,Tags[?Key=='Name']|[0].Value || 'unnamed']" \
    --output text | while read -r id az cidr public name; do
        label="$id ($name, $az, $cidr"
        [[ "$public" == "True" ]] && label="$label, public"
        echo "$label)"
    done)

if [[ -z "$SUBNET_LIST" ]]; then
    die "No subnets found in VPC $VPC_ID."
fi

lines_to_array SUBNET_OPTIONS "$SUBNET_LIST"
pick_from_list SUBNET_CHOICE "Select a subnet (public recommended):" "${SUBNET_OPTIONS[@]}"
SUBNET_ID=$(echo "$SUBNET_CHOICE" | cut -d' ' -f1)
log_success "Subnet: $SUBNET_ID"

# --- Discover Route53 zones ---
log_info "Discovering Route53 hosted zones..."
ZONE_LIST=$(aws route53 list-hosted-zones \
    --query "HostedZones[?Config.PrivateZone==\`false\`].[Id,Name]" \
    --output text | while read -r id name; do
        id="${id#/hostedzone/}"
        echo "$id ($name)"
    done)

ZONE_ID=""
if [[ -n "$ZONE_LIST" ]]; then
    lines_to_array ZONE_OPTIONS "$ZONE_LIST"
    ZONE_OPTIONS[${#ZONE_OPTIONS[@]}]="Skip — no DNS"
    pick_from_list ZONE_CHOICE "Select a Route53 hosted zone for DNS:" "${ZONE_OPTIONS[@]}"
    if [[ "$ZONE_CHOICE" != "Skip — no DNS" ]]; then
        ZONE_ID=$(echo "$ZONE_CHOICE" | cut -d' ' -f1)
        log_success "Route53 zone: $ZONE_ID"
    else
        log_info "Skipping DNS"
    fi
else
    log_warn "No Route53 hosted zones found. DNS will not be configured."
fi

# --- Check/create instance profile ---
INSTANCE_PROFILE="vmcluster-instance"
log_info "Checking IAM instance profile '$INSTANCE_PROFILE'..."

if aws iam get-instance-profile --instance-profile-name "$INSTANCE_PROFILE" &>/dev/null; then
    log_success "Instance profile exists"
else
    log_warn "Instance profile '$INSTANCE_PROFILE' not found."
    echo ""
    echo "  The vmcluster worker needs an IAM instance profile for EC2 instances."
    echo "  Run './infra/ops setup' to create it, or create it manually."
    echo ""
    die "Instance profile not found. See infra/README or run './infra/ops setup'."
fi

# --- Create worker in ConfigHub ---
echo ""
echo -e "${BOLD}ConfigHub worker setup${NC}"
echo ""

# List spaces
log_info "Fetching spaces..."
SPACE_LIST=$(cub space list --output json 2>/dev/null | python3 -c "
import sys, json
spaces = json.load(sys.stdin)
for s in spaces:
    print(s.get('Slug', ''))
" 2>/dev/null || echo "")

WORKER_SPACE=""
if [[ -n "$SPACE_LIST" ]]; then
    lines_to_array SPACE_OPTIONS "$SPACE_LIST"
    pick_from_list WORKER_SPACE "Which space should the vmcluster worker belong to?" "${SPACE_OPTIONS[@]}"
else
    prompt WORKER_SPACE "Space slug for the vmcluster worker"
fi

prompt WORKER_SLUG "Worker slug" "vmcluster-controller"

log_info "Creating worker '$WORKER_SLUG' in space '$WORKER_SPACE'..."
WORKER_OUTPUT=$(cub worker create "$WORKER_SLUG" --space "$WORKER_SPACE" --output json 2>&1) || {
    # Worker might already exist
    if echo "$WORKER_OUTPUT" | grep -qi "already exists"; then
        log_warn "Worker '$WORKER_SLUG' already exists."
        echo "  If you need the credentials, delete and recreate it."
        die "Cannot retrieve secret for existing worker."
    fi
    die "Failed to create worker: $WORKER_OUTPUT"
}

WORKER_ID=$(echo "$WORKER_OUTPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('BridgeWorkerID',''))")
WORKER_SECRET=$(echo "$WORKER_OUTPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('Secret',''))")

if [[ -z "$WORKER_ID" || -z "$WORKER_SECRET" ]]; then
    die "Failed to parse worker credentials from: $WORKER_OUTPUT"
fi

log_success "Worker created: $WORKER_ID"

# --- Write .env file ---
ENV_FILE=".env"
cat > "$ENV_FILE" <<EOF
# cub-vmcluster configuration
# Generated by bootstrap.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ)
CONFIGHUB_URL=$CUB_SERVER
CONFIGHUB_WORKER_ID=$WORKER_ID
CONFIGHUB_WORKER_SECRET=$WORKER_SECRET
SUBNET_ID=$SUBNET_ID
INSTANCE_PROFILE_NAME=$INSTANCE_PROFILE
ROUTE53_HOSTED_ZONE_ID=$ZONE_ID
AWS_REGION=$AWS_REGION
EOF

# Build docker run command based on AWS auth method
DOCKER_CMD="docker run --rm --env-file .env"
case "$AWS_AUTH_METHOD" in
    env)
        # Pass through env vars
        cat >> "$ENV_FILE" <<EOF
AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID
AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY
EOF
        [[ -n "${AWS_SESSION_TOKEN:-}" ]] && echo "AWS_SESSION_TOKEN=$AWS_SESSION_TOKEN" >> "$ENV_FILE"
        ;;
    profile)
        # Mount AWS config and pass profile
        echo "AWS_PROFILE=$AWS_PROFILE" >> "$ENV_FILE"
        DOCKER_CMD="$DOCKER_CMD -v \$HOME/.aws:/root/.aws:ro"
        ;;
    sso)
        # SSO needs the full ~/.aws directory (config + sso cache)
        DOCKER_CMD="$DOCKER_CMD -v \$HOME/.aws:/root/.aws:ro"
        ;;
    default)
        # Static creds in ~/.aws/credentials
        DOCKER_CMD="$DOCKER_CMD -v \$HOME/.aws:/root/.aws:ro"
        ;;
esac

DOCKER_CMD="$DOCKER_CMD ghcr.io/jesperfj/cub-vmcluster:latest"

log_success "Configuration written to $ENV_FILE"

# --- Summary ---
echo ""
echo "============================================"
echo -e "  ${GREEN}${BOLD}Bootstrap complete${NC}"
echo "============================================"
echo ""
echo "  Worker:   $WORKER_SLUG ($WORKER_ID)"
echo "  Space:    $WORKER_SPACE"
echo "  Subnet:   $SUBNET_ID"
echo "  Profile:  $INSTANCE_PROFILE"
[[ -n "$ZONE_ID" ]] && echo "  DNS Zone: $ZONE_ID"
echo ""
echo "  Start the worker with:"
echo ""
echo "    $DOCKER_CMD"
echo ""
echo "  Then create a VMCluster unit in ConfigHub and apply it."
echo "  See examples/test1.yaml for a sample configuration."
echo ""
