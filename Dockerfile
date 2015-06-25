# boilerpale
FROM kilianciuffolo/static
MAINTAINER me@nailik.org

RUN npm install -g rtail

# app
WORKDIR /website
ADD package.json ./
RUN npm install && npm cache clean

ADD . ./
RUN node_modules/.bin/gulp
CMD http-server dist | sed -ue 's/\[.*\] //g' | sed -u 's/\x1b\[[0-9;]*m//g' | rtail --id rtail.org --host rtail-demo.lukibear.com
EXPOSE 8080