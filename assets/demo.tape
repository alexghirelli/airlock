# The demo GIF in the README. Render it with scripts/demo.sh (it prepares a
# throwaway project, a host home with decoy secrets and a ready sandbox first, so no
# image build shows up):
#
#   scripts/demo.sh            # needs vhs, ttyd, ffmpeg, Docker and the airlock binary

Output assets/demo.gif

Set Shell "bash"
Set FontSize 17
Set Width 1200
Set Height 650
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 28ms
Set PlaybackSpeed 1

# Setup the viewer does not see.
Hide
Type 'cd "$AIRLOCK_DEMO_DIR" && export HOME="$AIRLOCK_DEMO_HOME" PS1="\[\e[32m\]myapp\[\e[0m\] $ " && clear'
Enter
Sleep 500ms
Show

# ---- 1. The agent goes rogue ------------------------------------------------
Type "# my machine has secrets"
Enter
Type "ls -a ~ | grep -E 'ssh|aws'"
Enter
Sleep 1.5s

Type "# an agent in airlock, prompt-injected, goes looking for them"
Enter
Type "airlock exec -- cat ~/.ssh/id_ed25519"
Enter
Sleep 2s
Type "airlock exec -- curl -sS -m 3 http://192.168.1.1"
Enter
Sleep 2.5s
Type "airlock exec -- sudo iptables -F"
Enter
Sleep 2s
Type "airlock exec -- sh -c 'echo evil >> .git/hooks/pre-commit'"
Enter
Sleep 2.5s
Type "# the internet still works, so it can do its job"
Enter
Type "airlock exec -- curl -sI https://github.com | head -1"
Enter
Sleep 3s
Ctrl+L

# ---- 2. Parallel work -------------------------------------------------------
Type "# meanwhile, review a pull request next to my own work"
Enter
Type "airlock review pr:7 'check the edge cases' --no-attach"
Enter
Sleep 4s
Type "airlock wt ls"
Enter
Sleep 3s

Type "airlock wt open review-pr-7"
Enter
Sleep 4s
# Herdr's welcome box and its integrations page, shown once per sandbox.
Enter
Sleep 1s
Escape
Sleep 7s
