## rtail website

### Install and initial setup

    $ npm install
    $ gulp app

This will spin up a local server with livereload.

### Create dist version

    $ gulp build:dist

### Docker
This website is deployed using a `Dockerfile` included in this repo. If you want to check that everything is working properly you can build and run the image yourself. If you need guidance on how to install docker you can read here: http://boot2docker.io

    $ docker build -t rtail/website .
    $ docker run --rm -p 8000:8000 --name rtail-website rtail/website
    $ open http://$(boot2docker ip):8888
    $ docker stop rtail-website
