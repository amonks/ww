Output screenshots/pool.gif
Set Shell bash
Set FontSize 16
Set Width 1400
Set Height 420
Set Padding 24
Set WindowBar Colorful
Set TypingSpeed 35ms

Hide
Type "source screenshots/setup.bash && clear" Enter
Wait@60s /app>$/
Show

Type "ww acquire --purpose 'fix the flaky test'" Sleep 400ms Enter Sleep 1s
Type "ww acquire --purpose 'add dark mode'" Sleep 400ms Enter Sleep 1s
Type "ww list" Sleep 400ms Enter Sleep 2.5s
Type "ww release ws-001" Sleep 400ms Enter Sleep 1s
Type "ww acquire --purpose 'write the README'" Sleep 400ms Enter Sleep 1s
Type "ww list" Sleep 400ms Enter Sleep 5s
