FROM python:3.12-slim-trixie@sha256:5e2bdb1b0d542d5039cffb8181c65930093949194a65c365dfb1b0fe13a4fa5b

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PYTHONPATH=/opt/tgcloud:/opt/tgcloud/app

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates rclone tini tzdata \
    && groupadd --system --gid 10001 tgcloud \
    && useradd --system --uid 10001 --gid 10001 --home-dir /nonexistent \
        --shell /usr/sbin/nologin tgcloud \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/tgcloud
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app ./app
# Public source must be readable by UID 10001 even from a private CLI staging
# directory. Keep root ownership: the runtime user must not modify this code.
RUN chmod -R a+rX app

USER 10001:10001

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["python", "-m", "tgcloud.app"]
