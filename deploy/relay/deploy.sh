#!/bin/sh
# Deploy germ's relay to Fly.io.
#
#   deploy/relay/deploy.sh [app-name]    # default: germ-relay-$USER
#
# Needs flyctl (https://fly.io/docs/flyctl/install/) and `fly auth login`.
set -eu
cd "$(dirname "$0")/../.."
app=${1:-germ-relay-$(whoami)}

if ! fly status --app "$app" >/dev/null 2>&1; then
  echo "creating Fly app $app"
  fly apps create "$app"
fi
# One machine only: rooms live in the relay's memory, so a host and its
# guests must reach the same instance (Fly would otherwise start two).
fly deploy . --app "$app" --config deploy/relay/fly.toml \
  --dockerfile deploy/relay/Dockerfile --ha=false

url="https://$app.fly.dev"
echo
if curl -fsS "$url/healthz" >/dev/null 2>&1; then
  echo "relay is up: $url"
else
  echo "deployed; it may take a few seconds to answer: $url/healthz"
fi
echo
echo "Add to ~/.config/germ/config.toml:"
echo
echo "  [share]"
echo "  relay = \"$url\""
