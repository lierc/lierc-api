FROM debian:stable
EXPOSE 5004
WORKDIR /opt/lierc-api
RUN apt-get update
RUN apt-get -y install carton build-essential libpq-dev
COPY . /opt/lierc-api
RUN carton install
RUN curl -s https://s3.amazonaws.com/bitly-downloads/nsq/nsq-0.3.8.linux-amd64.go1.6.2.tar.gz | tar -xvzf - -C /tmp
RUN mv /tmp/nsq-0.3.8.linux-amd64.go1.6.2/bin/nsq* /usr/local/bin
CMD ["carton", "exec", "plackup", "--server", "Gazelle", "-Ilib", "--max-workers", "4", "--listen", ":5004", "bin/app.psgi"]
