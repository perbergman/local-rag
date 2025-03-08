FROM python:3.9-slim

WORKDIR /app

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    python3-dev \
    && rm -rf /var/lib/apt/lists/*

# Create log directory
RUN mkdir -p /app/logs && chmod 777 /app/logs

# Install Python dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application code
COPY . .

# Set environment variables
ENV PYTHONUNBUFFERED=1
ENV PYTHONDONTWRITEBYTECODE=1

# Set memory limits for the JVM (used by some Hugging Face components)
ENV JAVA_OPTS="-Xms64m -Xmx512m"

# Set up health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
  CMD curl -f http://localhost:8080/health || exit 1

# Set up command with production server and reasonable defaults
CMD ["python", "embedding_service.py", "--workers", "4", "--log-level", "INFO"]
