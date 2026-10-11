# Sourced, hidden, at the top of every tape. The pool keeps its state and
# its workspaces under $HOME, so the recording gets a HOME of its own and
# never touches the operator's pool.
#
# The path is fixed rather than mktemp'd because it's on screen: a short
# one keeps the printed workspace paths readable. Creating it with a plain
# mkdir makes it a lock: a second recording, or a stale directory, makes
# this fail rather than delete someone else's demo, and only the shell
# that created it removes it.
#
# Every step returns on failure, so a broken setup never reaches the
# prompt the tapes wait for, and VHS fails instead of recording errors.
demo=/tmp/ww-demo
mkdir "$demo" || return
trap 'rm -rf "$demo"' EXIT
mkdir "$demo/bin" "$demo/app" || return
GOBIN="$demo/bin" go install . || return
export PATH="$demo/bin:$PATH"
export HOME="$demo"
export JJ_CONFIG="$demo/jj.toml"
cat >"$JJ_CONFIG" <<'TOML' || return
user.name = "Demo"
user.email = "demo@example.com"
ui.paginate = "never"
templates.log = "builtin_log_oneline"
TOML

cd "$demo/app" || return
jj git init --quiet || return
echo "# app" >README.md || return
echo ".env" >.gitignore || return
echo "API_KEY=hunter2" >.env || return
cat >ww.toml <<'TOML' || return
[workspace]
on-create = "cp ~/app/.env ."
TOML
jj commit --quiet -m "initial commit" || return
jj bookmark create --quiet main -r @- || return
# Only once main exists, or every jj command above warns that it can't
# resolve trunk(). The pool resets a released workspace to trunk().
echo 'revset-aliases."trunk()" = "main"' >>"$JJ_CONFIG" || return

export PS1='\[\e[38;2;90;86;224m\]\W>\[\e[0m\] '
