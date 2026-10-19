"""lover-instance nlp-service.

A stateless FastAPI micro-service that scores batches of Chinese chat
messages with a self-built dictionary baseline. It NEVER opens a database
file and never speaks SQLite: Go owns all persistence.
"""