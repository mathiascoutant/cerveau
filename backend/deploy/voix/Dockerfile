FROM python:3.13-alpine

# Version figée : le service de Microsoft change sans prévenir, edge-tts suit.
# Quand les voix se taisent, monter cette version et reconstruire.
RUN pip install --no-cache-dir edge-tts==7.3.1 \
    && adduser -D -H -u 10002 voix

COPY voix.py /app/voix.py
USER voix
EXPOSE 8080
CMD ["python", "-I", "/app/voix.py"]
