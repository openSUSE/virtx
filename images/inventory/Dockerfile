# build
FROM registry.opensuse.org/opensuse/bci/golang:latest AS builder

ADD https://releases.hashicorp.com/serf/0.8.2/serf_0.8.2_linux_amd64.zip /

RUN zypper -n ref && \
    zypper -n install \
        libvirt-devel \
        unzip && \
    unzip -d /usr/bin serf_0.8.2_linux_amd64.zip && \
    chmod +x /usr/bin/serf

COPY . /src
WORKDIR /src

RUN make

# run
FROM registry.opensuse.org/opensuse/tumbleweed:latest

COPY --from=builder /usr/bin/serf /usr/bin
COPY --from=builder /src/inventory-service /usr/bin
COPY images/inventory/entrypoint.sh /

RUN zypper -n ref && \
    zypper -n install \
        libvirt-libs && \
    chmod +x /entrypoint.sh

# default serf ports
EXPOSE 7946/tcp 7946/udp
# web server
EXPOSE 8080/tcp

ENTRYPOINT [ "/entrypoint.sh" ]
