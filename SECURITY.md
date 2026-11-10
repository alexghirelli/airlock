# Security policy

airlock's purpose is containment, so reports about ways out of the sandbox are
welcome, especially:

- reaching the host, the local network or cloud metadata from inside a sandbox;
- changing the firewall or regaining dropped capabilities from inside;
- writing to paths that should be read-only (`.git/hooks`, `.git/config`, mounts
  declared `ro`);
- a project's `.airlock.toml` taking effect beyond its allowed keys;
- credentials or tokens leaking between sandboxes or to the host.

Please report privately to the repository owner rather than opening a public
issue, and include the airlock version, the engine (Docker version, rootless or
not, OS) and the smallest steps to reproduce. There is no bug bounty.

Known limits, such as the shared host kernel of the container backend, are listed
under "What it does not protect against" in the README and are not vulnerabilities.
