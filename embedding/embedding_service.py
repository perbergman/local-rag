# embedding_service.py
# Simple Flask service to generate embeddings using a local Sentence Transformers model

from flask import Flask, request, jsonify
from sentence_transformers import SentenceTransformer
import logging
import argparse

app = Flask(__name__)

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger('embedding_service')

# Initialize the model (this will be done when the service starts)
model = None

@app.route('/embeddings', methods=['POST'])
def get_embeddings():
    """Generate embeddings for provided texts."""
    if request.method == 'POST':
        try:
            # Parse JSON request
            data = request.get_json()
            
            if not data or 'texts' not in data:
                return jsonify({'error': 'Missing texts in request'}), 400
                
            texts = data['texts']
            
            if not isinstance(texts, list):
                return jsonify({'error': 'Texts must be a list'}), 400
                
            if len(texts) == 0:
                return jsonify({'embeddings': []}), 200
                
            # Generate embeddings
            logger.info(f"Generating embeddings for {len(texts)} texts")
            embeddings = model.encode(texts)
            
            # Convert numpy arrays to lists for JSON serialization
            embeddings_list = embeddings.tolist()
            
            logger.info(f"Generated {len(embeddings_list)} embeddings")
            
            return jsonify({'embeddings': embeddings_list})
            
        except Exception as e:
            logger.error(f"Error generating embeddings: {e}")
            return jsonify({'error': str(e)}), 500

def start_service(model_name, host, port):
    """Start the embedding service."""
    global model
    
    logger.info(f"Loading model: {model_name}")
    model = SentenceTransformer(model_name)
    logger.info("Model loaded successfully")
    
    logger.info(f"Starting server on {host}:{port}")
    app.run(host=host, port=port)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='Start the embedding service')
    parser.add_argument('--model', default='all-MiniLM-L6-v2', help='Model name or path')
    parser.add_argument('--host', default='0.0.0.0', help='Host to bind the server to')
    parser.add_argument('--port', type=int, default=8080, help='Port to bind the server to')
    
    args = parser.parse_args()
    
    start_service(args.model, args.host, args.port)
