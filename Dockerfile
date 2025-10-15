ARG VERSION=0.0.0

FROM --platform=$BUILDPLATFORM node:lts AS uibuilder
ENV PNPM_HOME="/pnpm"
ENV PATH="$PNPM_HOME:$PATH"
RUN corepack enable pnpm && corepack install -g pnpm@latest-9

WORKDIR /src
COPY ui .
RUN pnpm install && pnpm build

FROM --platform=$BUILDPLATFORM tonistiigi/xx:1.6.1 AS xx

FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS gobuilder
ARG VERSION
ARG TARGETPLATFORM
WORKDIR /src

RUN echo "deb http://deb.debian.org/debian testing main" >> /etc/apt/sources.list && \
    apt-get update && \
    apt-get install -y -t testing gcc-13 g++-13 && \
    apt-get install -y git make wget && \
    rm -rf /var/lib/apt/lists/*

RUN wget -q https://github.com/Kitware/CMake/releases/download/v3.30.0/cmake-3.30.0-linux-$(uname -m).tar.gz && \
    tar xzf cmake-3.30.0-linux-$(uname -m).tar.gz -C /usr/local --strip-components=1 && \
    rm cmake-3.30.0-linux-$(uname -m).tar.gz

COPY --from=xx / /

ARG TARGETPLATFORM
RUN xx-apt-get install -y gcc-13 g++-13 libc6-dev libstdc++-13-dev

# Clone librm_lines from external repo
RUN git clone --depth 1 --branch crdt-id https://github.com/rmitchellscott/librm_lines.git /tmp/librm_lines

# Build librm_lines as static library for target platform
RUN cd /tmp/librm_lines && \
    mkdir -p build && cd build && \
    CC="$(xx-info)-gcc-13" CXX="$(xx-info)-g++-13" cmake -DCMAKE_BUILD_TYPE=Release \
          -DBUILD_SHARED_LIBS=OFF \
          .. && \
    make -j$(nproc) rm_lines && \
    mkdir -p /src/internal/rmlines/librm_lines/lib /src/internal/rmlines/librm_lines/include && \
    find . -name "*.o" | xargs $(xx-info)-ar rcs /src/internal/rmlines/librm_lines/lib/librm_lines.a && \
    cp -r ../rm_lines/headers /src/internal/rmlines/librm_lines/include/

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=uibuilder /src/dist ./ui/dist

RUN --mount=type=cache,target=/root/.cache \
    export CC="$(xx-info)-gcc-13" && \
    export CXX="$(xx-info)-g++-13" && \
    export STDC_PATH=$(find $(xx-info sysroot)/usr/lib/gcc/$(xx-info)/13 -name "libstdc++.a" | head -1) && \
    export CGO_LDFLAGS="-L/src/internal/rmlines/librm_lines/lib -lrm_lines -Wl,--whole-archive ${STDC_PATH} -Wl,--no-whole-archive -lm -static-libgcc" && \
    CGO_ENABLED=1 xx-go build \
    -ldflags="-s -w -linkmode external -extldflags '-static -Wl,--allow-multiple-definition' -X main.version=${VERSION}" \
    -trimpath \
    -o /rmfakecloud ./cmd/rmfakecloud/ && \
    xx-verify --static /rmfakecloud

FROM scratch AS binaries
COPY --from=gobuilder /rmfakecloud /rmfakecloud

FROM scratch AS final
EXPOSE 3000
ADD ./docker/rootfs.tar /
COPY --from=gobuilder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=gobuilder /rmfakecloud /rmfakecloud
ENTRYPOINT ["/rmfakecloud"]
