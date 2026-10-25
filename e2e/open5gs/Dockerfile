FROM ubuntu:24.04

LABEL org.opencontainers.image.source="https://github.com/ellanetworks/ims"

# Unreleased upstream main: v2.8.0's HSS rejects IMPIs with digits in the
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

# ifc.patch makes the HSS give the iFCs of the MCPTT AS (provision.js) in the User-Data of the SAA (TS 29.228
# Annex B.2.2): the SPTs of each iFC, not only the first's (lib/dbi/ims.c set the count of the first iFC only), and
# the Include Register Request and Response of an application server (tApplicationServerExtension), which the
# MCPTT AS's service authorisation needs (TS 24.379 §7.3.2, TS 24.229 §5.4.1.7A). Upstream: open5gs/open5gs#4897,
# open5gs/open5gs#4898; drop the patch once both are fixed.
COPY ifc.patch /ifc.patch

# qos.patch makes the PCRF and the PCF authorise the media of an MCPTT call (TS 29.213 Table 6.3.1, TS 29.513
# Table 7.3.3-1): the application media component of the floor control channel, which they refuse (5004
# DIAMETER_INVALID_AVP_VALUE, 400 "Unknown Media-Type"), on QCI/5QI 69, and the voice on 65 (TS 23.379 §5.7.3), when
# the AF-Application-Identifier or afAppId is the MCPTT ICSI (TS 29.214 Annex A.13.1 NOTE 3, TS 29.514 Annex B.10.1
# NOTE 3); otherwise audio stays on 1 and application gets 2; an application component without a PCC rule of its
# QCI/5QI stays on the default bearer or QoS flow. Not upstream yet.
COPY qos.patch /qos.patch

# signalling.patch makes the PCRF and the PCF take the media component of the P-CSCF's subscription to the signalling
# path status (TS 29.214 §4.4.5, TS 29.514 §4.2.6.7), of Flow-Usage AF_SIGNALLING, Flow-Number 0 and no Media-Type,
# for session binding only: the PCF refused it (400 "Media-Type is Required"), the PCRF took it for AUDIO. Not
# upstream yet.
COPY signalling.patch /signalling.patch

RUN git clone https://github.com/open5gs/open5gs /src \
 && git -C /src checkout "$OPEN5GS_REF" \
 && git -C /src apply /ifc.patch /qos.patch /signalling.patch \
 && cd /src \
 && meson setup build --prefix=/usr --sysconfdir=/etc --localstatedir=/var \
 && ninja -C build install \
 && rm -rf /src /ifc.patch /qos.patch /signalling.patch
