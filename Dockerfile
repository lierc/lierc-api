ARG PERL_VERSION=5.44.0

FROM ubuntu:latest AS builder
ARG PERL_VERSION
RUN apt-get update && apt-get install -y \
    build-essential \
    curl \
    libssl-dev \
    zlib1g-dev \
    perl \
    cpanminus \
    && rm -rf /var/lib/apt/lists/*
RUN cpanm Perl::Build
RUN perl-build -j4 $PERL_VERSION /opt/perl-$PERL_VERSION

FROM ubuntu:latest AS base
ARG PERL_VERSION
COPY --from=builder /opt/perl-$PERL_VERSION /opt/perl-$PERL_VERSION
ENV PATH="/opt/perl-${PERL_VERSION}/bin:${PATH}"
WORKDIR /opt/lierc-api
RUN apt-get update && apt-get -y install curl build-essential libpq-dev zip libssl-dev zlib1g-dev
RUN curl -s https://cpanmin.us/ > /opt/perl-${PERL_VERSION}/bin/cpanm \
    && chmod +x /opt/perl-${PERL_VERSION}/bin/cpanm
RUN cpanm --self-upgrade
RUN cpanm -nq Carmel
COPY cpanfile cpanfile.snapshot /opt/lierc-api/
RUN carmel install
RUN carmel rollout

FROM ubuntu:latest AS api
ARG PERL_VERSION
RUN apt-get update && apt-get install -y \
    libpq5 \
    libssl3t64 \
    && rm -rf /var/lib/apt/lists/*
COPY . /opt/lierc-api
COPY --from=base /opt/lierc-api/local /opt/lierc-api/local
COPY --from=base /opt/perl-$PERL_VERSION /opt/perl-$PERL_VERSION
ENV PATH="/opt/perl-${PERL_VERSION}/bin:${PATH}"
EXPOSE 5004
WORKDIR /opt/lierc-api
# LISTEN sets the address, e.g. 127.0.0.1:5004; sh fills it in and exec
# keeps plackup as PID 1, so it gets docker's stop signal.
CMD ["sh", "-c", "exec perl -Ilocal/lib/perl5 local/bin/plackup --server Gazelle -Ilib --max-workers 4 --listen \"${LISTEN:-:5004}\" bin/api.psgi"]
