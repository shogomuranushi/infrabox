FROM ubuntu:24.04
LABEL org.opencontainers.image.source=https://github.com/shogomuranushi/infrabox

RUN apt-get update && apt-get install -y --no-install-recommends \
    sudo curl wget git vim nano \
    build-essential python3 python3-pip \
    nodejs npm jq unzip htop tmux \
    ripgrep fd-find fzf zsh \
  && rm -rf /var/lib/apt/lists/*

# GitHub CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg \
  && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
  && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
  && apt-get update && apt-get install -y --no-install-recommends gh \
  && rm -rf /var/lib/apt/lists/*

# uv (Python package manager)
RUN curl -LsSf https://astral.sh/uv/install.sh | sh \
  && mv /root/.local/bin/uv /usr/local/bin/uv \
  && mv /root/.local/bin/uvx /usr/local/bin/uvx

# Claude Code (native installer — move binary to /usr/local/bin for all users)
RUN curl -fsSL https://claude.ai/install.sh | bash \
  && mv /root/.local/bin/claude /usr/local/bin/claude

# Codex
RUN npm install -g @openai/codex

# User setup
RUN (id ubuntu &>/dev/null || useradd -m -s /bin/zsh ubuntu) \
  && usermod -s /bin/zsh ubuntu \
  && echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers

# rclone (Google Drive sync for context sharing)
RUN curl https://rclone.org/install.sh | bash

EXPOSE 8000
CMD ["sleep", "infinity"]
