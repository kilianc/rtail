# boilerplate
FROM kilianciuffolo/node
MAINTAINER me@nailik.org

RUN npm install -g rtail

# app
WORKDIR /website
COPY package.json ./
RUN npm install && npm cache clean

COPY . ./
RUN node_modules/.bin/gulp

ENV DEBUG express:*
ENV DEBUG_COLORS 1
ENV NODE_ENV production
CMD node --harmony server 2>&1 | rtail --id rtail.org --host rtail-demo.lukibear.com
EXPOSE 8080
