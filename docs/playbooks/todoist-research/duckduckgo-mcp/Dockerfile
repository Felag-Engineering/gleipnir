FROM node:22-alpine
# Add Python + uv so supergateway can spawn uvx duckduckgo-mcp-server.
RUN apk add --no-cache python3 py3-pip && \
    pip install --break-system-packages uv
