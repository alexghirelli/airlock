# airlock base image: Pi, Herdr, mise and a hardened entrypoint on Debian.
#
# Built by `airlock build` from the files embedded in the airlock binary; the
# build context is this directory. The image is generic: the host UID/GID is
# applied when the container starts, not baked in.

ARG NODE_VERSION=22

# ---- Pi runs on its own private Node, so project toolchains installed with mise
# ---- can never shadow or break it. Other agents (Claude Code, other Pi versions)
# ---- are not here: they are installed into the tools volume when needed.
FROM node:${NODE_VERSION}-trixie-slim AS agents
ARG PI_PACKAGE=@earendil-works/pi-coding-agent
ARG PI_VERSION
RUN test -n "${PI_VERSION}" \
 && npm install -g --ignore-scripts --prefix /opt/pi "${PI_PACKAGE}@${PI_VERSION}" \
 && cp "$(command -v node)" /opt/pi/node \
 && cp -r /usr/local/lib/node_modules/npm /opt/pi/npm \
 && mkdir -p /opt/pi/shims && ln -s ../node /opt/pi/shims/node \
 && echo "${PI_VERSION}" > /opt/pi/VERSION && echo "${PI_PACKAGE}" > /opt/pi/PACKAGE

# ---- runtime ----
FROM debian:trixie-slim
ARG EXTRA_APT=""

ENV LANG=C.UTF-8 \
    HOME=/home/agent \
    PATH=/home/agent/.local/share/mise/shims:/home/agent/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    PI_CODING_AGENT_DIR=/creds/pi-agent \
    CLAUDE_CONFIG_DIR=/creds/claude \
    DISABLE_AUTOUPDATER=1 \
    PI_TEAM_DIR=/home/agent/.pi-team \
    MISE_YES=1 \
    MISE_IDIOMATIC_VERSION_FILE_ENABLE_TOOLS=node,python,ruby,go

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl wget git sudo iptables iproute2 jq ripgrep fd-find less nano \
      procps unzip xz-utils bzip2 build-essential pkg-config gnupg file libcap2-bin \
 && if [ -n "${EXTRA_APT}" ]; then apt-get install -y --no-install-recommends ${EXTRA_APT}; fi \
 && ln -s "$(command -v fdfind)" /usr/local/bin/fd \
 && rm -rf /var/lib/apt/lists/*

# Non-root user with passwordless sudo, inside the container only. The entrypoint
# re-maps its UID/GID to the host user's at start.
RUN useradd -m -s /bin/bash -u 1000 agent \
 && echo 'agent ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/agent \
 && chmod 0440 /etc/sudoers.d/agent \
 && mkdir -p /etc/airlock /creds /mnt /home/agent/.local/bin /home/agent/.local/share/mise \
 && chown -R agent:agent /creds /home/agent

# Pi gets a wrapper that calls its private node directly. `claude` is a wrapper too:
# the program itself is installed on first use (agent-install.sh), never baked in.
COPY --from=agents /opt/pi /opt/pi
COPY pi-wrapper.sh /usr/local/bin/pi
COPY claude-wrapper.sh /usr/local/bin/claude
COPY agent-install.sh /usr/local/bin/airlock-agent-install
COPY npm-wrapper.sh /opt/pi/shims/npm
RUN chmod 0755 /usr/local/bin/pi /usr/local/bin/claude /usr/local/bin/airlock-agent-install /opt/pi/shims/npm \
 && pi --version

# mise (toolchains per project) and Herdr (agent multiplexer), both system-wide.
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh \
 && mise --version \
 && curl -fsSL https://herdr.dev/install.sh | HERDR_INSTALL_DIR=/usr/local/bin sh \
 && herdr --version

# Team tooling (optional at runtime; see `airlock --team`).
COPY team/pi-team /usr/local/bin/pi-team
COPY team/profiles/ /opt/airlock/team/profiles/
RUN chmod 0755 /usr/local/bin/pi-team

COPY entrypoint.sh /usr/local/sbin/airlock-entrypoint
COPY init.sh /usr/local/bin/airlock-init
COPY session.sh /usr/local/bin/airlock-session
COPY herdr-up.sh /usr/local/bin/airlock-herdr-up
COPY profile-path.sh /etc/profile.d/10-airlock-path.sh
RUN chmod 0755 /usr/local/sbin/airlock-entrypoint /usr/local/bin/airlock-init /usr/local/bin/airlock-session /usr/local/bin/airlock-herdr-up

# Earlier root-run steps may have created files under the agent's home (mise
# needs a writable ~/.cache, for one); hand it all back.
RUN chown -R agent:agent /home/agent

USER root
WORKDIR /
ENTRYPOINT ["/usr/local/sbin/airlock-entrypoint"]
CMD ["sleep", "infinity"]
