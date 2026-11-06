FROM debian:bookworm-slim

# Prepare this target-architecture binary explicitly in a Linux development
# Guest before building the image; see docs/MANAGED-PROJECTS.md.
COPY --chmod=0755 reach-helper-linux /usr/local/bin/reach-helper

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
