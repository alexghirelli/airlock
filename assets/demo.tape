# The demo GIF in the README. Render it with scripts/demo.sh (it prepares a
# throwaway project and a ready sandbox first, so no image build shows up):
#
#   scripts/demo.sh            # needs vhs, ttyd, ffmpeg, Docker and the airlock binary

Output assets/demo.gif

Set Shell "bash"
Set FontSize 18
Set Width 1200
Set Height 650
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 45ms
Set PlaybackSpeed 1

# Setup the viewer does not see.
Hide
Type 'cd "$AIRLOCK_DEMO_DIR" && export PS1="\[\e[32m\]myapp\[\e[0m\] $ " && clear'
Enter
Sleep 500ms
Show

# 1. The agent is not root, and not you.
Type "airlock exec -- whoami"
Enter
Sleep 2s

# 2. The internet works.
Type "airlock exec -- curl -sI https://github.com | head -1"
Enter
Sleep 2.5s

# 3. Your network does not.
Type "airlock exec -- curl -m 3 http://192.168.1.1"
Enter
Sleep 3s

# 4. Work: Herdr with Pi in the project.
Type "airlock"
Enter
Sleep 4s
# Herdr's welcome box and its integrations page, shown once per sandbox.
Enter
Sleep 1s
Escape
Sleep 6s
