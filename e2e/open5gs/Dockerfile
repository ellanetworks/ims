FROM ubuntu:24.04

LABEL org.opencontainers.image.source="https://github.com/ellanetworks/ims"

# Unreleased upstream main: v2.8.0 HSS rejects IMPIs with digits in the
# realm (open5gs 100ee681e, "hss/cx: Stop IMSI extraction at IMPI realm separator").
ARG OPEN5GS_REF=4107085fefa91b84ca7c81986c61ab8bd0319e67

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential ninja-build meson cmake flex bison git pkg-config ca-certificates \
      libsctp-dev libgnutls28-dev libgcrypt-dev libssl-dev libmongoc-dev libbson-dev \
      libyaml-dev libmicrohttpd-dev libcurl4-gnutls-dev libnghttp2-dev libtins-dev \
      libtalloc-dev libc-ares-dev libidn-dev \
      iproute2 iputils-ping tcpdump procps \
 && rm -rf /var/lib/apt/lists/*

RUN git clone https://github.com/open5gs/open5gs /src \
 && git -C /src checkout "$OPEN5GS_REF" \
 && cd /src \
 && meson setup build --prefix=/usr --sysconfdir=/etc --localstatedir=/var \
 && ninja -C build install \
 && rm -rf /src
