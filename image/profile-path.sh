# Debian's /etc/profile resets PATH for login shells (`airlock shell`, Herdr
# panes), which would hide the tools installed with mise. Put them back.
for d in /home/agent/.local/bin /home/agent/.local/share/mise/shims; do
  case ":$PATH:" in *":$d:"*) ;; *) PATH="$d:$PATH" ;; esac
done
export PATH
