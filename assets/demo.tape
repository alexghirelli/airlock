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
Set TypingSpeed 24ms
Set PlaybackSpeed 1

# Setup the viewer does not see.
Hide
Type 'cd "$AIRLOCK_DEMO_DIR" && source "$AIRLOCK_DEMO_RC" && card "airlock" "any agent, in a container you control. Docker is all you need."; sleep 3; clear'
Enter
Show
Sleep 3.3s

# ---- 1. The agent goes rogue ------------------------------------------------
Hide
Type 'card "1 · a prompt-injected agent goes looking for your secrets" "inside airlock, with the same commands any agent could run"; sleep 3; clear'
Enter
Show
Sleep 3.3s

Type "# my machine has secrets"
Enter
Type "ls -a ~ | grep -E 'ssh|aws'"
Enter
Sleep 1.2s
Type "airlock exec -- cat ~/.ssh/id_ed25519"
Enter
Sleep 1.8s
Type "airlock exec -- curl -sS -m 3 http://192.168.1.1"
Enter
Sleep 2s
Type "airlock exec -- sudo iptables -F"
Enter
Sleep 1.8s
Type "airlock exec -- sh -c 'echo evil >> .git/hooks/pre-commit'"
Enter
Sleep 2s
Type "airlock exec -- curl -sI https://github.com | head -1"
Enter
Sleep 2.5s

# ---- 2. Parallel work -------------------------------------------------------
Hide
Type 'card "2 · review a pull request next to your own work" "Gerrit, GitHub, GitLab or Bitbucket, on a local branch, one agent per worktree"; sleep 3; clear'
Enter
Show
Sleep 3.3s

Type "airlock review pr:7 'check the edge cases' --no-attach"
Enter
Sleep 3.5s
Type "airlock wt ls"
Enter
Sleep 2.5s

Type "airlock wt open review-pr-7"
Enter
Sleep 4s
# Herdr's welcome box and its integrations page, shown once per sandbox.
Enter
Sleep 1s
Escape
Sleep 7s
