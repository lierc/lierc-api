FROM debian:jessie
EXPOSE 5004
WORKDIR /opt/lierc-api
RUN apt-get update
RUN apt-get -y install carton build-essential libpq-dev
RUN curl -s https://s3.amazonaws.com/bitly-downloads/nsq/nsq-1.0.0-compat.linux-amd64.go1.8.tar.gz | tar -xvzf - -C /tmp
RUN mv /tmp/nsq-1.0.0-compat.linux-amd64.go1.8/bin/nsq* /usr/local/bin
COPY cpanfile /opt/lierc-api/cpanfile
RUN carton install
COPY . /opt/lierc-api
CMD ["carton", "exec", "plackup", "--server", "Gazelle", "-Ilib", "--max-workers", "4", "--listen", ":5004", "bin/app.psgi"]
