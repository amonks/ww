# ww

A pool of [jj](https://jj-vcs.github.io/jj/) workspaces, so many agents (or
builds, or people) can work in one repo at once without trampling each other.

    go install monks.co/ww@latest

![ww acquire hands out a workspace per purpose; ww release returns one to the pool for the next acquire to reuse](screenshots/pool.gif)

![ww exec runs a command in a workspace and releases it on exit; the change made there stays in the repo](screenshots/exec.gif)

![ww.toml's on-create hook runs in each workspace as it's handed out](screenshots/hooks.gif)
