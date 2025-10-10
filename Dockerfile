ARG VERSION=0.0.0
FROM --platform=$BUILDPLATFORM node:lts AS uibuilder
ENV PNPM_HOME="/pnpm"
ENV PATH="$PNPM_HOME:$PATH"
RUN corepack enable pnpm && corepack install -g pnpm@latest-9

WORKDIR /src
#COPY ui/package.json ui/pnpm-lock.yaml /src
#RUN pnpm fetch 

COPY ui .
RUN pnpm install && pnpm build

FROM golang:bookworm AS gobuilder
ARG VERSION
ARG TARGETPLATFORM
WORKDIR /src

# Install C++ build tools and CMake 3.30
# Add Debian testing for gcc-13 (needed for C++20 <format> support)
RUN echo "deb http://deb.debian.org/debian testing main" >> /etc/apt/sources.list && \
    apt-get update && \
    apt-get install -y -t testing gcc-13 g++-13 && \
    apt-get install -y cmake git wget && \
    rm -rf /var/lib/apt/lists/*

RUN wget -q https://github.com/Kitware/CMake/releases/download/v3.30.0/cmake-3.30.0-linux-$(uname -m).tar.gz && \
    tar xzf cmake-3.30.0-linux-$(uname -m).tar.gz -C /usr/local --strip-components=1 && \
    rm cmake-3.30.0-linux-$(uname -m).tar.gz

COPY . .
COPY --from=uibuilder /src/dist ./ui/dist

# Initialize submodules
RUN git submodule update --init --recursive

# Build librm_lines as STATIC library
RUN cd internal/rmlines/librm_lines && \
    mkdir -p build && cd build && \
    CC=gcc-13 CXX=g++-13 cmake -DCMAKE_BUILD_TYPE=Release \
          -DBUILD_SHARED_LIBS=OFF \
          .. && \
    make -j$(nproc) rm_lines

# Build rmfakecloud with static linking
RUN export CGO_ENABLED=1 && \
    export CC=gcc-13 && \
    export CXX=g++-13 && \
    export CGO_CXXFLAGS="-I/src/internal/rmlines/librm_lines/rm_lines/headers -std=c++20" && \
    export CGO_LDFLAGS="-L/src/internal/rmlines/librm_lines/build -lrm_lines -lstdc++ -lm -static -static-libgcc -static-libstdc++" && \
    go generate ./... && \
    go build -ldflags "-s -w -linkmode external -extldflags '-static' -X main.version=${VERSION}" \
    -o rmfakecloud-docker ./cmd/rmfakecloud/

# Build test-search program
RUN export CGO_ENABLED=1 && \
    export CC=gcc-13 && \
    export CXX=g++-13 && \
    export CGO_CXXFLAGS="-I/src/internal/rmlines/librm_lines/rm_lines/headers -std=c++20" && \
    export CGO_LDFLAGS="-L/src/internal/rmlines/librm_lines/build -lrm_lines -lstdc++ -lm -static -static-libgcc -static-libstdc++" && \
    go build -ldflags "-s -w -linkmode external -extldflags '-static'" -o test-search ./cmd/test-search/

FROM scratch
EXPOSE 3000
ADD ./docker/rootfs.tar /
COPY --from=gobuilder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=gobuilder /src/rmfakecloud-docker /
COPY --from=gobuilder /src/test-search /
ENTRYPOINT ["/rmfakecloud-docker"]
