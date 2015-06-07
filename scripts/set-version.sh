set -e

if [ ! -z "$NPM_PUBLISH" ]; then
  echo "setting VERSION env"
  export VERSION=`node -e "console.log(require('./package.json').version)"`
  export AWS_BUCKET_URL="$AWS_BUCKET_URL/$VERSION/"
fi