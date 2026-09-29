Output docs/demo/demo.gif
Require zakwas
Set Shell bash
Set FontSize 16
Set Width 1100
Set Height 640
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms
Hide
Type "export HOME=$(mktemp -d) PS1='$ ' && cp -R docs/demo $HOME/dotfiles && cd $HOME/dotfiles && clear"
Enter
Show
Type "zakwas plan"
Enter
Sleep 2.5s
Type "zakwas apply -y"
Enter
Sleep 2.5s
Type "echo 'alias x=y' >> ~/.zshrc"
Enter
Sleep 1.5s
Type "zakwas check; echo exit $?"
Enter
Sleep 3s
Hide
Type "chflags -R nouchg $HOME && rm -rf $HOME"
Enter
