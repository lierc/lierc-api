FROM debian:stable
EXPOSE 5004
WORKDIR /opt/lierc-api
COPY . /opt/lierc-api
RUN apt-get update
RUN apt-get -y install carton build-essential libpq-dev
RUN carton install
RUN cp config.example.json config.json
CMD ["carton", "exec", "plackup", "--server", "Gazelle", "-Ilib", "--max-workers", "4", "--listen", ":5004", "bin/app.psgi"]
