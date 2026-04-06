FROM golang:1.25.8-bookworm

ENV WORKING_DIR=/usr/app/src/
ENV TZ=Africa/Lagos
ENV JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64
ENV PATH="${PATH}:/root/.local/bin:${JAVA_HOME}/bin"

WORKDIR ${WORKING_DIR}

# Install dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        gnupg \
        unzip \
        make \
        build-essential \
        python3 \
        python3-pip \
        python3-venv \
        sshpass \
        ansible \
        docker.io \
        openjdk-17-jdk && \
    rm -rf /var/lib/apt/lists/*

# Python venv (clean installs)
RUN python3 -m venv /opt/venv && \
    /opt/venv/bin/pip install --no-cache-dir docker

# Node.js (for API testing / tooling)
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y nodejs && \
    npm install -g newman newman-reporter-htmlextra && \
    rm -rf /var/lib/apt/lists/*

# Go tools (CI helpers)
RUN go install golang.org/x/tools/cmd/goimports@latest && \
    go install github.com/go-delve/delve/cmd/dlv@latest

# Sonar Scanner (for code quality)
RUN curl -sSLo sonar-scanner.zip https://binaries.sonarsource.com/Distribution/sonar-scanner-cli/sonar-scanner-cli-5.0.1.3006-linux.zip && \
    unzip sonar-scanner.zip && \
    mv sonar-scanner-* /opt/sonar-scanner && \
    ln -s /opt/sonar-scanner/bin/sonar-scanner /usr/local/bin/sonar-scanner && \
    rm sonar-scanner.zip

# Docker group access
RUN groupadd -f docker && usermod -aG docker root

CMD ["bash"]