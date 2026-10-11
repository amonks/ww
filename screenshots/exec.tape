Output screenshots/exec.gif
Set Shell bash
Set FontSize 16
Set Width 960
Set Height 440
Set Padding 24
Set WindowBar Colorful
Set TypingSpeed 35ms

Hide
Type "source screenshots/setup.bash && clear" Enter
Wait@60s /app>$/
Show

Type "ww exec -- bash" Sleep 400ms Enter Sleep 1.5s
Type "echo hello >hello.txt" Sleep 400ms Enter Sleep 500ms
Type "jj commit -m 'say hello'" Sleep 400ms Enter Sleep 1.5s
Type "exit" Sleep 400ms Enter Sleep 1s
Type "jj log" Sleep 400ms Enter Sleep 5s
