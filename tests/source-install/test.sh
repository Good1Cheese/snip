#!/bin/sh

set -eu

repo_root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
resolver="$repo_root/scripts/install-path"
tmp_dir=$(CDPATH='' cd -- "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp_dir"' EXIT

fake_bin="$tmp_dir/bin"
mkdir -p "$fake_bin"
test_path="$fake_bin:/usr/bin:/bin"

cat >"$fake_bin/go" <<'EOF'
#!/bin/sh
case "$1 $2" in
  "env GOBIN")
    printf '%s\n' "${TEST_GO_GOBIN:-}"
    ;;
  "env GOPATH")
    printf '%s\n' "${TEST_GO_GOPATH:-}"
    ;;
  *)
    exit 2
    ;;
esac
EOF
chmod +x "$fake_bin/go"

assert_destination() {
	name=$1
	want=$2
	shift 2

	got=$("$@")
	if [ "$got" != "$want" ]; then
		printf '%s: got %s, want %s\n' "$name" "$got" "$want" >&2
		exit 1
	fi
}

existing_bin="$tmp_dir/existing/bin"
mkdir -p "$existing_bin"
cat >"$existing_bin/snip" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$existing_bin/snip"

assert_destination "explicit GOBIN" "$tmp_dir/explicit dir/snip" \
	env GOBIN="$tmp_dir/explicit dir" PATH="$existing_bin:$fake_bin" \
	TEST_GO_GOBIN="$tmp_dir/go-bin" TEST_GO_GOPATH="$tmp_dir/go-path" \
	"$resolver"

assert_destination "explicit GOBIN upgrade" "$tmp_dir/explicit dir/snip" \
	env GOBIN="$tmp_dir/explicit dir" PATH="$existing_bin:$fake_bin" \
	TEST_GO_GOBIN="$tmp_dir/go-bin" TEST_GO_GOPATH="$tmp_dir/go-path" \
	"$resolver" --upgrade

assert_destination "install ignores existing snip" "$tmp_dir/go-bin/snip" \
	env GOBIN= PATH="$existing_bin:$test_path" \
	TEST_GO_GOBIN="$tmp_dir/go-bin" TEST_GO_GOPATH="$tmp_dir/go-path" \
	"$resolver"

assert_destination "upgrade existing snip" "$existing_bin/snip" \
	env GOBIN= PATH="$existing_bin:$test_path" \
	TEST_GO_GOBIN="$tmp_dir/go-bin" TEST_GO_GOPATH="$tmp_dir/go-path" \
	"$resolver" --upgrade

assert_destination "go env GOBIN" "$tmp_dir/go-bin/snip" \
	env GOBIN= PATH="$test_path" \
	TEST_GO_GOBIN="$tmp_dir/go-bin" TEST_GO_GOPATH="$tmp_dir/go-path" \
	"$resolver"

assert_destination "upgrade without existing snip" "$tmp_dir/go-bin/snip" \
	env GOBIN= PATH="$test_path" \
	TEST_GO_GOBIN="$tmp_dir/go-bin" TEST_GO_GOPATH="$tmp_dir/go-path" \
	"$resolver" --upgrade

assert_destination "first GOPATH entry" "$tmp_dir/go-path-one/bin/snip" \
	env GOBIN= PATH="$test_path" \
	TEST_GO_GOBIN="" TEST_GO_GOPATH="$tmp_dir/go-path-one:$tmp_dir/go-path-two" \
	"$resolver"

error_file="$tmp_dir/error"
if env GOBIN= PATH="$test_path" TEST_GO_GOBIN="" TEST_GO_GOPATH="" \
	"$resolver" >"$tmp_dir/output" 2>"$error_file"; then
	printf 'empty Go environment unexpectedly resolved a destination\n' >&2
	exit 1
fi

if [ -s "$tmp_dir/output" ]; then
	printf 'empty Go environment produced a destination: %s\n' "$(cat "$tmp_dir/output")" >&2
	exit 1
fi

if ! grep -q "set GOBIN or configure the Go environment" "$error_file"; then
	printf 'empty Go environment did not produce an actionable error\n' >&2
	exit 1
fi

if "$resolver" --unknown >"$tmp_dir/output" 2>"$error_file"; then
	printf 'unknown resolver option unexpectedly succeeded\n' >&2
	exit 1
else
	status=$?
fi
if [ "$status" -ne 2 ] || ! grep -q 'usage: install-path' "$error_file"; then
	printf 'unknown resolver option did not produce a usage error\n' >&2
	exit 1
fi

symlink_target="$tmp_dir/releases/0.25.0/snip-real"
mkdir -p "$(dirname "$symlink_target")" "$tmp_dir/current/bin"
cp "$existing_bin/snip" "$symlink_target"
ln -s "../../releases/0.25.0/snip-real" "$tmp_dir/current/bin/snip"
assert_destination "existing snip symlink" "$symlink_target" \
	env GOBIN= PATH="$tmp_dir/current/bin:$test_path" \
	TEST_GO_GOBIN="$tmp_dir/go-bin" TEST_GO_GOPATH="$tmp_dir/go-path" \
	"$resolver" --upgrade

for target in install install-lite upgrade upgrade-lite; do
	recipe=$(make -C "$repo_root" -n "$target")
	if ! printf '%s\n' "$recipe" | grep -q 'scripts/install-path'; then
		printf '%s does not use the shared destination resolver\n' "$target" >&2
		exit 1
	fi
	if printf '%s\n' "$recipe" | grep -q '/usr/local/bin'; then
		printf '%s still contains the privileged fallback\n' "$target" >&2
		exit 1
	fi
	if ! printf '%s\n' "$recipe" | grep -q 'mv -f.*temporary.*destination'; then
		printf '%s does not replace the destination atomically\n' "$target" >&2
		exit 1
	fi
	if ! printf '%s\n' "$recipe" | grep -q 'mktemp'; then
		printf '%s uses a predictable temporary path\n' "$target" >&2
		exit 1
	fi
done

make_project="$tmp_dir/make-project"
mkdir -p "$make_project/scripts"
cp "$repo_root/Makefile" "$make_project/Makefile"
cp "$resolver" "$make_project/scripts/install-path"
printf '#!/bin/sh\nprintf updated\n' >"$make_project/snip"
chmod +x "$make_project/snip"

if PATH="$test_path" GOBIN='' TEST_GO_GOBIN='' TEST_GO_GOPATH='' \
	make -s -C "$make_project" -o build install \
	>"$tmp_dir/install-output" 2>"$tmp_dir/install-error"; then
	printf 'install unexpectedly succeeded without a destination\n' >&2
	exit 1
fi
if [ -s "$tmp_dir/install-output" ]; then
	printf 'install continued after destination resolution failed\n' >&2
	exit 1
fi
if ! grep -q "no install destination" "$tmp_dir/install-error"; then
	printf 'install did not report destination resolution failure\n' >&2
	exit 1
fi

blocked_parent="$tmp_dir/not-a-directory"
printf 'blocked\n' >"$blocked_parent"
if make -s -C "$make_project" -o build install \
	GOBIN="$blocked_parent/child" >"$tmp_dir/install-output" 2>"$tmp_dir/install-error"; then
	printf 'install unexpectedly succeeded with an invalid destination\n' >&2
	exit 1
fi
if ! grep -Fq "Installing $blocked_parent/child/snip" "$tmp_dir/install-output"; then
	printf 'failed install did not print its resolved destination\n' >&2
	exit 1
fi
if ! grep -Fq "cannot create install directory $blocked_parent/child" "$tmp_dir/install-error"; then
	printf 'failed install did not produce an actionable error\n' >&2
	exit 1
fi

for target in install install-lite upgrade upgrade-lite; do
	case "$target" in
		install|upgrade) build_target=build ;;
		install-lite|upgrade-lite) build_target=build-lite ;;
	esac
	install_dir="$tmp_dir/$target destination"
	make -s -C "$make_project" -o "$build_target" "$target" GOBIN="$install_dir"
	if ! cmp -s "$make_project/snip" "$install_dir/snip"; then
		printf '%s did not install the requested binary\n' "$target" >&2
		exit 1
	fi
	if [ ! -x "$install_dir/snip" ]; then
		printf '%s did not install an executable binary\n' "$target" >&2
		exit 1
	fi
done

printf '#!/bin/sh\nprintf old\n' >"$symlink_target"
PATH="$tmp_dir/current/bin:$test_path" GOBIN='' \
	TEST_GO_GOBIN="$tmp_dir/default-install" TEST_GO_GOPATH="$tmp_dir/go-path" \
	make -s -C "$make_project" -o build install
if ! grep -q old "$symlink_target"; then
	printf 'install unexpectedly changed the active snip target\n' >&2
	exit 1
fi
if ! cmp -s "$make_project/snip" "$tmp_dir/default-install/snip"; then
	printf 'install did not use the Go-configured destination\n' >&2
	exit 1
fi

PATH="$tmp_dir/current/bin:$test_path" GOBIN='' \
	make -s -C "$make_project" -o build upgrade
if [ ! -L "$tmp_dir/current/bin/snip" ]; then
	printf 'upgrade replaced the active snip symlink\n' >&2
	exit 1
fi
if ! cmp -s "$make_project/snip" "$symlink_target"; then
	printf 'upgrade did not replace the active snip symlink target\n' >&2
	exit 1
fi

printf 'source-install destination tests passed\n'
