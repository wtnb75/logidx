FROM scratch
COPY logidx /logidx
ENTRYPOINT ["/logidx"]
