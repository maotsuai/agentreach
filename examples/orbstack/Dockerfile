FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       bash ca-certificates coreutils git python3 ripgrep tmux \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 developer \
    && useradd --uid 10001 --gid 10001 --create-home --shell /bin/bash developer \
    && mkdir -p /workspace/project \
    && chown 10001:10001 /workspace/project

USER 10001:10001
WORKDIR /workspace/project
CMD ["sleep", "infinity"]
