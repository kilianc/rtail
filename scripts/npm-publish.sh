set -e

if [ ! -n "$NPM_PUBLISH" ]; then
  echo "Skipping npm publish, NPM_PUBLISH is not set."
  exit 0
fi

if [ ! -n "$NPM_TOKEN" ]; then
  echo "Please set NPM_TOKEN env variable"
  exit 1
fi

if [ ! -n "$NPM_EMAIL" ]; then
  echo "Please set NPM_EMAIL env variable"
  exit 1
fi

echo "_auth = $NPM_TOKEN" >  ~/.npmrc
echo "email = $NPM_EMAIL" >> ~/.npmrc

npm publish