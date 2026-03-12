FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*

RUN git clone https://github.com/rldiao/mealie-mcp-server /app

WORKDIR /app

COPY recipe_models_patch.py /app/src/models/recipe.py

RUN pip install --no-cache-dir mcp-proxy -e .

EXPOSE 8092

# mcp-proxy wraps the mcp stdio server and exposes Streamable HTTP at /mcp
# Requires MEALIE_BASE_URL and MEALIE_API_KEY env vars — server exits immediately without them
ENTRYPOINT ["mcp-proxy", "--host", "0.0.0.0", "--port", "8092", "--pass-environment", "--", "python", "src/server.py"]
