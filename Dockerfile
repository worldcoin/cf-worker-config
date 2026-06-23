FROM public.ecr.aws/docker/library/python:3.12-slim
    #checkov:skip=CKV_DOCKER_2:Kubernetes not Docker

RUN useradd -m app
RUN mkdir -p /srv/http-server/ready
RUN chown -R app:app /srv/http-server

# Create a startup script
RUN echo '#!/bin/sh\npython -m http.server 8080 & \npython -m http.server 8081 & \nwait' > /srv/http-server/start.sh
RUN chmod +x /srv/http-server/start.sh
RUN chown app:app /srv/http-server/start.sh

WORKDIR /srv/http-server/
USER app
    
CMD ["/srv/http-server/start.sh"]
