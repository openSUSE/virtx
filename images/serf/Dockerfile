FROM registry.opensuse.org/opensuse/tumbleweed:latest

ADD https://releases.hashicorp.com/serf/0.8.2/serf_0.8.2_linux_amd64.zip /

RUN zypper -n install unzip && \
    unzip -d /usr/bin serf_0.8.2_linux_amd64.zip && \
    chmod +x /usr/bin/serf

ENTRYPOINT [ "/usr/bin/serf" ]
