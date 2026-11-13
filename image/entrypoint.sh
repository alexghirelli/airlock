#!/usr/bin/env bash
# Runs as root once per container start:
#   1. map the `agent` user to the host user's UID/GID
#   2. install the egress policy (fail closed)
#   3. drop NET_ADMIN/NET_RAW from the bounding set of everything started here
#   4. hand over to the unprivileged `agent` user
#
# `docker exec` does NOT inherit this process's bounding set: exec'd processes
# get the container's configured capabilities. airlock therefore starts every
# session through /usr/local/bin/airlock-session, which drops them again.
#
# The policy comes from the container's environment, which is fixed when the
# container is created and cannot be changed from inside:
#   AIRLOCK_NET_MODE     open-no-lan | allowlist | open
#   AIRLOCK_ALLOW_CIDRS  comma-separated CIDRs exempt from the LAN block
#   AIRLOCK_PROXY        host:port of the filtering proxy (allowlist mode)
#   AIRLOCK_UID/GID      host user ids
set -euo pipefail

# This script is root with NET_ADMIN, and it runs again at every restart of the
# container, before the firewall is back. The image's PATH starts with folders the
# agent can write (~/.local/bin and the mise shims, the latter on a volume shared by
# all projects), so every program looked up through it here, from `id` to `iptables`,
# would be the agent's. Everything below uses root-owned folders only; the agent's
# own PATH is put back for the process that is handed over at the end.
AGENT_PATH="$PATH"
export PATH=/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin

say() { echo "airlock: $*" >&2; }
die() { say "$*"; exit 1; }

# ---------------------------------------------------------------- user mapping
remap_user() {
  local uid="${AIRLOCK_UID:-1000}" gid="${AIRLOCK_GID:-1000}"
  [[ "$uid" =~ ^[0-9]+$ && "$gid" =~ ^[0-9]+$ ]] || die "invalid AIRLOCK_UID/AIRLOCK_GID"
  # Root on the host maps to the default unprivileged id.
  [[ "$uid" == 0 ]] && uid=1000
  [[ "$gid" == 0 ]] && gid=1000
  local cur_uid cur_gid
  cur_uid="$(id -u agent)"; cur_gid="$(id -g agent)"
  if [[ "$gid" != "$cur_gid" ]]; then groupmod -o -g "$gid" agent; fi
  if [[ "$uid" != "$cur_uid" ]]; then usermod -o -u "$uid" -g "$gid" agent; fi
  # Fix ownership of what the image put in the home directory (not of mounts).
  if [[ "$uid" != "$cur_uid" || "$gid" != "$cur_gid" ]]; then
    find /home/agent -xdev \( -user "$cur_uid" -o -group "$cur_gid" \) -exec chown -h "$uid:$gid" {} +
  fi
  # Named volumes are created root-owned on first use, or owned by an older id.
  local d
  for d in /creds /home/agent/.local/share/mise; do
    [[ -d "$d" ]] || continue
    if [[ "$(stat -c %u "$d")" != "$uid" ]]; then chown -R "$uid:$gid" "$d"; fi
  done
}

# -------------------------------------------------------------------- firewall
# Ranges the sandbox must not reach in open-no-lan mode: private, shared,
# link-local (cloud metadata), multicast, and the benchmarking range some
# desktop runtimes (OrbStack, Colima) use for the host.
V4_BLOCK=(0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 169.254.0.0/16 172.16.0.0/12 192.0.0.0/24
          192.168.0.0/16 198.18.0.0/15 224.0.0.0/4 240.0.0.0/4)
V6_BLOCK=(fc00::/7 fe80::/10 ff00::/8)

have_global_v6() {
  # /proc/net/if_inet6: addr idx plen scope flags name; scope 00 = global
  [[ -r /proc/net/if_inet6 ]] && awk '$4 == "00" { found = 1 } END { exit !found }' /proc/net/if_inet6
}

# Allowed CIDRs, split by address family.
ALLOW_V4=(); ALLOW_V6=()
parse_allow_cidrs() {
  local IFS=, c
  for c in ${AIRLOCK_ALLOW_CIDRS:-}; do
    [[ -n "$c" ]] || continue
    if [[ "$c" == *:* ]]; then ALLOW_V6+=("$c"); else ALLOW_V4+=("$c"); fi
  done
}

firewall_common() {
  local ipt="$1"
  $ipt -w -F OUTPUT
  $ipt -w -A OUTPUT -o lo -j ACCEPT
  $ipt -w -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
}

# DNS. Docker's embedded resolver (127.0.0.11, reachable through lo) forwards
# upstream queries from *inside this container's network namespace*, so they
# pass through this firewall too. The upstream servers are listed in the
# resolv.conf header ("# ExtServers: [...]") and are often private addresses
# (the docker0 bridge, the host's resolver, a router). Other runtimes (Podman,
# the default bridge) put them in "nameserver" lines. Allow port 53 to exactly
# those servers and nothing else.
dns_servers() {
  local conf="${1:-/etc/resolv.conf}"
  awk '$1 == "nameserver" { print $2 }' "$conf"
  # The header lists the upstream servers in forms like "[172.17.0.1 8.8.8.8]" or,
  # on Docker Desktop, "[host(192.168.65.7)]". Pull the addresses out and ignore
  # the wrapping; anything that is not an address is dropped, never passed on.
  sed -n 's/^# *ExtServers: *\[\(.*\)\].*/\1/p' "$conf" |
    grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}|[0-9a-fA-F]*:[0-9a-fA-F:]+' || true
}

# Accept only plain IPv4/IPv6 literals as firewall arguments.
is_ip() { [[ "$1" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ || "$1" =~ ^[0-9a-fA-F]*:[0-9a-fA-F:]+$ ]]; }

allow_dns() {
  local ns
  while read -r ns; do
    is_ip "$ns" || continue
    case "$ns" in
      127.*|::1|'') continue ;;
      *:*) if have_ip6; then
             ip6tables -w -A OUTPUT -d "$ns" -p udp --dport 53 -j ACCEPT
             ip6tables -w -A OUTPUT -d "$ns" -p tcp --dport 53 -j ACCEPT
           fi ;;
      *) iptables -w -A OUTPUT -d "$ns" -p udp --dport 53 -j ACCEPT
         iptables -w -A OUTPUT -d "$ns" -p tcp --dport 53 -j ACCEPT ;;
    esac
  done < <(dns_servers | sort -u)
}

have_ip6() { ip6tables -w -L OUTPUT >/dev/null 2>&1; }

firewall_open_no_lan() {
  local c net
  firewall_common iptables
  for c in ${ALLOW_V4[@]+"${ALLOW_V4[@]}"}; do iptables -w -A OUTPUT -d "$c" -j ACCEPT; done
  allow_dns
  # The container's own subnets (sibling sandboxes) and its gateway (the host),
  # even when the runtime uses address ranges outside the list below.
  while read -r net; do
    [[ -n "$net" && "$net" != default ]] && iptables -w -A OUTPUT -d "$net" -j REJECT
  done < <(ip -4 -o route show scope link | awk '{ print $1 }')
  while read -r net; do
    [[ -n "$net" ]] && iptables -w -A OUTPUT -d "$net/32" -j REJECT
  done < <(ip -4 route show default | awk '{ print $3 }')
  for net in "${V4_BLOCK[@]}"; do iptables -w -A OUTPUT -d "$net" -j REJECT; done

  if have_ip6; then
    firewall_common ip6tables
    for c in ${ALLOW_V6[@]+"${ALLOW_V6[@]}"}; do ip6tables -w -A OUTPUT -d "$c" -j ACCEPT; done
    allow_dns
    for net in "${V6_BLOCK[@]}"; do ip6tables -w -A OUTPUT -d "$net" -j REJECT; done
  elif have_global_v6; then
    die "the container has IPv6 but ip6tables is unavailable; refusing to start"
  fi
}

firewall_allowlist() {
  local host port ip
  [[ -n "${AIRLOCK_PROXY:-}" ]] || die "allowlist mode needs AIRLOCK_PROXY"
  host="${AIRLOCK_PROXY%:*}"; port="${AIRLOCK_PROXY##*:}"
  [[ "$port" =~ ^[0-9]+$ ]] || die "invalid AIRLOCK_PROXY: $AIRLOCK_PROXY"
  ip="$(getent ahostsv4 "$host" | awk 'NR == 1 { print $1 }')"
  [[ -n "$ip" ]] || die "cannot resolve the proxy $host"
  firewall_common iptables
  allow_dns
  # Direct egress is rejected; the only way out is the filtering proxy.
  iptables -w -A OUTPUT -d "$ip" -p tcp --dport "$port" -j ACCEPT
  iptables -w -A OUTPUT -j REJECT
  if have_ip6; then
    firewall_common ip6tables
    ip6tables -w -A OUTPUT -j REJECT
  elif have_global_v6; then
    die "the container has IPv6 but ip6tables is unavailable; refusing to start"
  fi
  echo "$ip" >/run/airlock/proxy-ip
}

verify_firewall() {
  # Fail closed: the rules we meant to install must really be there.
  iptables -w -S OUTPUT | grep -q -- '-j REJECT' || die "firewall verification failed (no REJECT rule)"
}

install_firewall() {
  parse_allow_cidrs
  case "${AIRLOCK_NET_MODE:-open-no-lan}" in
    open)
      say "WARNING: network mode 'open': no egress restrictions. The host, your LAN and cloud metadata are reachable." ;;
    open-no-lan)
      firewall_open_no_lan; verify_firewall
      say "firewall on (open-no-lan: internet only; host, LAN and metadata blocked)" ;;
    allowlist)
      firewall_allowlist; verify_firewall
      say "firewall on (allowlist: egress only through the filtering proxy)" ;;
    *) die "unknown AIRLOCK_NET_MODE '${AIRLOCK_NET_MODE}'" ;;
  esac
}

# ------------------------------------------------------------------------ main
main() {
  # The firewall runs in a child process: inside `if !` the shell would ignore
  # `set -e`, and a rule that failed to install would go unnoticed.
  if [[ "${1:-}" == __firewall ]]; then
    install_firewall
    exit 0
  fi

  mkdir -p /run/airlock
  remap_user
  # Global agent instructions: Pi loads context files from every parent directory
  # of its working directory, so one at / reaches every project path and, unlike
  # a file in the (shared) credentials volume, belongs to this sandbox alone.
  install -m 0644 -o root -g root /etc/airlock/AGENTS.md /AGENTS.md
  if ! "$0" __firewall; then
    die "cannot set up the firewall (is NET_ADMIN granted?). Refusing to start."
  fi
  chown agent: /run/airlock
  echo "${AIRLOCK_NET_MODE:-open-no-lan}" >/run/airlock/net-mode

  gid="$(id -g agent)"
  exec /usr/bin/setpriv --reuid agent --regid "$gid" --init-groups \
    --bounding-set -net_admin,-net_raw \
    /usr/bin/env "PATH=$AGENT_PATH" /usr/local/bin/airlock-init "$@"
}

# Sourcing the file (tests) only defines the functions.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
