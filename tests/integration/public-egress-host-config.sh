# shellcheck shell=bash
# Match the root-provisioned managed ceiling in the full-suite VM while keeping
# older selected steps on their original NIC-blocked project.
set -euo pipefail
test "$#" -eq 1
if test "${P_TEST_PUBLIC_EGRESS_FIXTURE:-0}" != 1; then
  exit 0
fi
test -n "${P_TEST_NFT_BINARY:-}"
test -n "${P_TEST_NETWORK_PROOF_BINARY:-}"
host_config=$1
next="$host_config.egress-next"
jq --arg nft "$P_TEST_NFT_BINARY" --arg proof "$P_TEST_NETWORK_PROOF_BINARY" '
  .runtime.public_egress={network:"p-public-v1",acl:"p-public-v1-acl",
    bridge_ipv4:"10.233.0.1/24",dns:["1.1.1.1","9.9.9.9"],
    sudo_binary:"/run/wrappers/bin/sudo",nft_binary:$nft,
    bridge_proof_binary:$proof}
' "$host_config" > "$next"
mv -- "$next" "$host_config"
