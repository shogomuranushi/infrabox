FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    openssh-server sudo curl wget git vim nano \
    build-essential python3 python3-pip \
    nodejs npm jq unzip htop tmux \
  && rm -rf /var/lib/apt/lists/*

# SSH server setup
RUN mkdir -p /var/run/sshd \
  && (id ubuntu &>/dev/null || useradd -m -s /bin/bash ubuntu) \
  && usermod -s /bin/bash ubuntu \
  && echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers

EXPOSE 22 8000
CMD ["/usr/sbin/sshd", "-D"]
