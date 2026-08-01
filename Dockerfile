ARG VERSION=0.0.0

FROM --platform=$BUILDPLATFORM node:lts AS uibuilder
ENV PNPM_HOME="/pnpm"
ENV PATH="$PNPM_HOME:$PATH"
RUN corepack enable pnpm && corepack install -g pnpm@latest-9

WORKDIR /src
COPY ui .
RUN pnpm install && pnpm build

FROM --platform=$BUILDPLATFORM tonistiigi/xx:1.6.1 AS xx

FROM --platform=$BUILDPLATFORM golang:1.24-trixie AS gobuilder
ARG VERSION
ARG TARGETPLATFORM
WORKDIR /src

# Install build tools and compilers
# python3 is only used by librm_lines' cmake to generate emscripten export lists,
# but its find_package is unconditional, so configure fails without it.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        gcc-14 g++-14 \
        gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64 \
        cmake git make python3 && \
    rm -rf /var/lib/apt/lists/*

COPY --from=xx / /

# Install cross-compilation tools for target platform
ARG TARGETPLATFORM
RUN if echo "$TARGETPLATFORM" | grep -q "windows"; then \
        # librm_lines links DbgHelp by that exact casing, but mingw ships
        # libdbghelp.a, so the lookup misses on a case-sensitive filesystem
        ln -sf /usr/x86_64-w64-mingw32/lib/libdbghelp.a \
               /usr/x86_64-w64-mingw32/lib/libDbgHelp.a; \
    else \
        xx-apt-get install -y gcc-14 g++-14 libc6-dev libstdc++-14-dev; \
    fi

# Clone librm_lines from external repo
ARG LIBRM_LINES_REF=ec94a92b05a20b0a19be3a1e893208d46f43e03b
RUN git init /tmp/librm_lines && \
    cd /tmp/librm_lines && \
    git remote add origin https://github.com/RedTTGMoss/librm_lines.git && \
    git fetch --depth 1 origin ${LIBRM_LINES_REF} && \
    git checkout FETCH_HEAD

# librm_lines fetches cppcodec at GIT_TAG master, which would drift under us.
# Pre-seed it at a known commit; FETCHCONTENT_SOURCE_DIR_* makes cmake use this
# checkout instead of cloning. Its sibling deps are already tagged upstream.
ARG CPPCODEC_REF=8019b8b580f8573c33c50372baec7039dfe5a8ce
RUN git init /tmp/cppcodec && \
    cd /tmp/cppcodec && \
    git remote add origin https://github.com/tplgy/cppcodec.git && \
    git fetch --depth 1 origin ${CPPCODEC_REF} && \
    git checkout FETCH_HEAD

# Build librm_lines as static library for target platform
RUN cd /tmp/librm_lines && \
    mkdir -p build && cd build && \
    if echo "$TARGETPLATFORM" | grep -q "windows"; then \
        CC="x86_64-w64-mingw32-gcc" CXX="x86_64-w64-mingw32-g++" && \
        LDFLAGS="-ldbghelp" cmake -DCMAKE_BUILD_TYPE=Release \
              -DFETCHCONTENT_SOURCE_DIR_CPPCODEC=/tmp/cppcodec \
              -DCMAKE_SYSTEM_NAME=Windows \
              -DCMAKE_SYSTEM_PROCESSOR=x86_64 \
              -DCMAKE_C_COMPILER="$CC" \
              -DCMAKE_CXX_COMPILER="$CXX" \
              -DCMAKE_CXX_STANDARD_LIBRARIES="-ldbghelp" \
              .. && \
        make -j$(nproc) rm_lines; \
    else \
        CC="$(xx-info)-gcc-14" CXX="$(xx-info)-g++-14" && \
        cmake -DCMAKE_BUILD_TYPE=Release \
              -DFETCHCONTENT_SOURCE_DIR_CPPCODEC=/tmp/cppcodec \
              -DCMAKE_C_COMPILER="$CC" \
              -DCMAKE_CXX_COMPILER="$CXX" \
              -DBUILD_SHARED_LIBS=OFF \
              .. && \
        make -j$(nproc) rm_lines; \
    fi && \
    mkdir -p /src/internal/rmlines/librm_lines/lib /src/internal/rmlines/librm_lines/include && \
    find . -name "*.o" -o -name "*.obj" | xargs $(xx-info)-ar rcs /src/internal/rmlines/librm_lines/lib/librm_lines.a && \
    cp -r ../rm_lines/headers /src/internal/rmlines/librm_lines/include/

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=uibuilder /src/dist ./ui/dist

RUN --mount=type=cache,target=/root/.cache \
    if echo "$TARGETPLATFORM" | grep -q "windows"; then \
        export CC="x86_64-w64-mingw32-gcc" && \
        export CXX="x86_64-w64-mingw32-g++" && \
        export CGO_LDFLAGS="-L/src/internal/rmlines/librm_lines/lib -lrm_lines -lstdc++ -ldbghelp -lm -static" && \
        CGO_ENABLED=1 xx-go build \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -trimpath \
        -o /rmfakecloud.exe ./cmd/rmfakecloud/; \
    else \
        export CC="$(xx-info)-gcc-14" && \
        export CXX="$(xx-info)-g++-14" && \
        export STDC_PATH=$(find $(xx-info sysroot)/usr/lib/gcc/$(xx-info)/ -name "libstdc++.a" | head -1) && \
        export CGO_LDFLAGS="-L/src/internal/rmlines/librm_lines/lib -lrm_lines -Wl,--whole-archive ${STDC_PATH} -Wl,--no-whole-archive -lm -static-libgcc" && \
        CGO_ENABLED=1 xx-go build \
        -ldflags="-s -w -linkmode external -extldflags '-static -Wl,--allow-multiple-definition' -X main.version=${VERSION}" \
        -trimpath \
        -o /rmfakecloud ./cmd/rmfakecloud/ && \
        xx-verify --static /rmfakecloud; \
    fi

FROM scratch AS binaries
COPY --from=gobuilder /rmfakecloud* /

FROM scratch AS final
EXPOSE 3000
ADD ./docker/rootfs.tar /
COPY --from=gobuilder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=gobuilder /rmfakecloud /rmfakecloud
ENTRYPOINT ["/rmfakecloud"]
