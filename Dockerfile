FROM debian:stable
EXPOSE 5004
WORKDIR /opt/lierc-api

RUN apt-get update && apt-get -y install curl build-essential libpq-dev zip libssl-dev zlib1g-dev
RUN curl -s https://s3.amazonaws.com/bitly-downloads/nsq/nsq-1.2.1.linux-amd64.go1.16.6.tar.gz | tar -xvzf - -C /tmp
RUN mv /tmp/nsq-1.*/bin/nsq* /usr/local/bin

RUN curl -s https://cpanmin.us/ > /usr/local/bin/cpanm
RUN chmod +x /usr/local/bin/cpanm
RUN cpanm --self-upgrade
RUN cpanm -nq Carmel
COPY cpanfile cpanfile.snapshot /opt/lierc-api/
RUN carmel install
RUN carmel rollout
ENV LIERC_NO_SMTP=1

COPY . /opt/lierc-api
CMD ["carmel", "exec", "plackup", "--server", "Gazelle", "-Ilib", "--max-workers", "4", "--listen", ":5004", "bin/api.psgi"]
