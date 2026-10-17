Output docs/demo/demo.gif
Require zakwas
Set Shell bash
Set FontSize 16
Set Width 900
Set Height 600
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms
Set Framerate 15
Set CursorBlink false

Hide
Type "export HOME=$(mktemp -d /tmp/zakwas-demo.XXXXXX) PS1='$ ' GIT_PAGER=cat"
Enter
Type "case $HOME in /tmp/zakwas-demo.??????) ;; *) exit 1;; esac"
Enter
Type "cp -R docs/demo $HOME/dotfiles && cd $HOME/dotfiles && rm demo.gif demo.tape"
Enter
Type "git init -q -b main && git config user.name you && git config user.email you@example.com && git config commit.gpgsign false && git add -A && git commit -qm init"
Enter
Type "clear"
Enter
Show

Type "zakwas plan"
Enter
Sleep 5s

Ctrl+L
Type "zakwas apply -y"
Enter
Sleep 5s

Ctrl+L
Type "echo 'alias x=y' >> ~/.zshrc"
Enter
Sleep 1.5s
Type "zakwas check; echo exit $?"
Enter
Sleep 3.5s

Ctrl+L
Type "# an agent proposes a rule, it can't apply it"
Enter
Type "zakwas propose add-permission deny 'git push --force' | head -1"
Enter
Sleep 2s
Type "zakwas proposals"
Enter
Sleep 2.5s
Hide
Type "id=$(zakwas proposals | cut -d' ' -f1)"
Enter
Show
Ctrl+L
Type "# a person reviews the diff and plan, then approves"
Enter
Type "zakwas approve $id"
Enter
Sleep 6s
Type "y"
Enter
Sleep 4s

Ctrl+L
Type "git show --stat --format=%s"
Enter
Sleep 3.5s

Hide
Type "cd / && chflags -R nouchg $HOME && rm -rf $HOME"
Enter
