FROM ubuntu:24.04
LABEL org.opencontainers.image.source=https://github.com/shogomuranushi/infrabox

RUN apt-get update && apt-get install -y --no-install-recommends \
    sudo curl wget git vim nano \
    build-essential python3 python3-pip \
    nodejs npm jq unzip htop tmux \
    ripgrep fd-find fzf zsh cron \
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

# Codex
RUN npm install -g @openai/codex

# User setup
RUN (id ubuntu &>/dev/null || useradd -m -s /bin/zsh ubuntu) \
  && usermod -s /bin/zsh ubuntu \
  && echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers

# Claude Code
RUN npm install -g @anthropic-ai/claude-code

# Make ~/.local/bin available in login shells for all users
RUN echo 'export PATH="$HOME/.local/bin:$PATH"' > /etc/profile.d/local-bin.sh

# rclone (Google Drive sync for context sharing)
RUN curl https://rclone.org/install.sh | bash

WORKDIR /home/ubuntu
USER ubuntu

# Zsh configuration
RUN cat >> ~/.zshrc <<'EOF'

# History
HISTFILE=~/.zsh_history
HISTSIZE=10000
SAVEHIST=10000
setopt SHARE_HISTORY HIST_IGNORE_DUPS HIST_IGNORE_SPACE

# Completion
autoload -Uz compinit && compinit

# Git branch in prompt
autoload -Uz vcs_info
precmd_vcs_info() { vcs_info }
precmd_functions+=( precmd_vcs_info )
zstyle ':vcs_info:*' enable git
zstyle ':vcs_info:git:*' formats ' %F{magenta}(%b)%f'
setopt PROMPT_SUBST

# Shorten k8s pod hostname (remove trailing hash/random suffix)
_short_host() {
  local h="${HOST}"
  # Remove last two dash-separated segments (k8s pod suffix like -789d6d5dd4-xxwlx)
  local s="${h%-*-*}"
  echo "${s:-$h}"
}
SHORT_HOST="$(_short_host)"

# Prompt: user@shorthost ~/path (branch) %
PROMPT='%F{cyan}%n@${SHORT_HOST}%f:%F{yellow}%~%f${vcs_info_msg_0_} %# '

# Locale
export LANG=C.UTF-8
export LC_ALL=C.UTF-8
EOF

# tmux configuration
RUN echo 'set -g mouse on' > ~/.tmux.conf

EXPOSE 8000
CMD ["sh", "-c", "sudo cron && sleep infinity"]
