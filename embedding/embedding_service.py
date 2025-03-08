# embedding_service.py
# Enhanced Flask service to generate embeddings using a local Sentence Transformers model
# with improved logging, error handling, and monitoring

from flask import Flask, request, jsonify
from sentence_transformers import SentenceTransformer
import logging
import argparse
import time
import traceback
import sys
import gc
import os
import psutil
import numpy as np
from werkzeug.middleware.proxy_fix import ProxyFix

app = Flask(__name__)
app.wsgi_app = ProxyFix(app.wsgi_app)

# Configure logging with more detailed format
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - [%(filename)s:%(lineno)d] - %(message)s'
)
logger = logging.getLogger('embedding_service')

# Add file handler for persistent logging
os.makedirs('logs', exist_ok=True)
file_handler = logging.FileHandler('logs/embedding_service.log')
file_handler.setFormatter(logging.Formatter('%(asctime)s - %(name)s - %(levelname)s - [%(filename)s:%(lineno)d] - %(message)s'))
logger.addHandler(file_handler)

# Initialize the model (this will be done when the service starts)
model = None

# Track memory usage
def get_memory_usage():
    process = psutil.Process(os.getpid())
    memory_info = process.memory_info()
    return {
        'rss': memory_info.rss / (1024 * 1024),  # RSS in MB
        'vms': memory_info.vms / (1024 * 1024),  # VMS in MB
        'percent': process.memory_percent()
    }

@app.route('/health', methods=['GET'])
def health_check():
    """Simple health check endpoint"""
    memory = get_memory_usage()
    return jsonify({
        'status': 'healthy',
        'model_loaded': model is not None,
        'memory_usage_mb': memory['rss'],
        'memory_percent': memory['percent']
    })

@app.route('/embeddings', methods=['POST'])
def get_embeddings():
    """Generate embeddings for provided texts with enhanced logging and error handling."""
    start_time = time.time()
    request_id = f"req-{int(start_time * 1000)}"
    
    if request.method == 'POST':
        try:
            # Log memory usage before processing
            memory_before = get_memory_usage()
            logger.info(f"[{request_id}] Memory before processing: {memory_before['rss']:.2f} MB ({memory_before['percent']:.2f}%)")
            
            # Parse JSON request
            data = request.get_json()
            
            if not data or 'texts' not in data:
                logger.warning(f"[{request_id}] Missing texts in request")
                return jsonify({'error': 'Missing texts in request'}), 400
                
            texts = data['texts']
            
            if not isinstance(texts, list):
                logger.warning(f"[{request_id}] Texts must be a list, got {type(texts)}")
                return jsonify({'error': 'Texts must be a list'}), 400
                
            if len(texts) == 0:
                logger.info(f"[{request_id}] Received empty texts list")
                return jsonify({'embeddings': []}), 200
            
            # Log text lengths to help identify problematic inputs
            text_lengths = [len(text) for text in texts]
            logger.info(f"[{request_id}] Processing {len(texts)} texts. Lengths: min={min(text_lengths)}, max={max(text_lengths)}, avg={sum(text_lengths)/len(text_lengths):.2f}")
            
            # Generate embeddings with timeout handling
            logger.info(f"[{request_id}] Generating embeddings")
            
            # Process in smaller batches if texts are large
            batch_size = 10 if max(text_lengths) > 10000 else 50
            all_embeddings = []
            
            for i in range(0, len(texts), batch_size):
                batch = texts[i:i+batch_size]
                logger.info(f"[{request_id}] Processing batch {i//batch_size + 1}/{(len(texts) + batch_size - 1)//batch_size} with {len(batch)} texts")
                batch_start = time.time()
                batch_embeddings = model.encode(batch)
                batch_time = time.time() - batch_start
                logger.info(f"[{request_id}] Batch processed in {batch_time:.2f}s ({batch_time/len(batch):.4f}s per text)")
                all_embeddings.extend(batch_embeddings.tolist())
                
                # Force garbage collection after each batch
                gc.collect()
            
            # Convert numpy arrays to lists for JSON serialization
            embeddings_list = all_embeddings
            
            # Log memory usage after processing
            memory_after = get_memory_usage()
            logger.info(f"[{request_id}] Memory after processing: {memory_after['rss']:.2f} MB ({memory_after['percent']:.2f}%)")
            logger.info(f"[{request_id}] Memory change: {memory_after['rss'] - memory_before['rss']:.2f} MB")
            
            # Log total processing time
            total_time = time.time() - start_time
            logger.info(f"[{request_id}] Generated {len(embeddings_list)} embeddings in {total_time:.2f}s ({total_time/len(texts):.4f}s per text)")
            
            # Force garbage collection
            gc.collect()
            
            return jsonify({'embeddings': embeddings_list})
            
        except Exception as e:
            # Detailed error logging with stack trace
            error_trace = traceback.format_exc()
            logger.error(f"[{request_id}] Error generating embeddings: {str(e)}\n{error_trace}")
            
            # Log memory at time of error
            memory_error = get_memory_usage()
            logger.error(f"[{request_id}] Memory at error: {memory_error['rss']:.2f} MB ({memory_error['percent']:.2f}%)")
            
            # Force garbage collection
            gc.collect()
            
            return jsonify({
                'error': str(e),
                'error_type': type(e).__name__,
                'request_id': request_id
            }), 500

def start_service(model_name, host, port, workers=1):
    """Start the embedding service with enhanced monitoring."""
    global model
    
    # Log system information
    logger.info(f"Starting embedding service with Python {sys.version}")
    logger.info(f"Available memory: {psutil.virtual_memory().available / (1024*1024*1024):.2f} GB")
    logger.info(f"CPU count: {psutil.cpu_count()}")
    
    try:
        logger.info(f"Loading model: {model_name}")
        load_start = time.time()
        model = SentenceTransformer(model_name)
        load_time = time.time() - load_start
        logger.info(f"Model loaded successfully in {load_time:.2f}s")
        
        # Log model information
        memory_after_load = get_memory_usage()
        logger.info(f"Memory after model load: {memory_after_load['rss']:.2f} MB ({memory_after_load['percent']:.2f}%)")
        
        # Test the model with a simple input
        logger.info("Testing model with sample input")
        test_start = time.time()
        test_embedding = model.encode(["This is a test sentence."])
        test_time = time.time() - test_start
        logger.info(f"Model test successful in {test_time:.4f}s. Embedding shape: {test_embedding.shape}")
        
        # Start the server with better error handling
        logger.info(f"Starting server on {host}:{port} with {workers} workers")
        
        if workers > 1:
            # Use production server with multiple workers
            from waitress import serve
            serve(app, host=host, port=port, threads=workers)
        else:
            # Use Flask development server
            app.run(host=host, port=port, threaded=True)
            
    except Exception as e:
        error_trace = traceback.format_exc()
        logger.critical(f"Failed to start service: {str(e)}\n{error_trace}")
        sys.exit(1)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='Start the embedding service')
    parser.add_argument('--model', default='all-MiniLM-L6-v2', help='Model name or path')
    parser.add_argument('--host', default='0.0.0.0', help='Host to bind the server to')
    parser.add_argument('--port', type=int, default=8080, help='Port to bind the server to')
    parser.add_argument('--workers', type=int, default=4, help='Number of worker threads for production server')
    parser.add_argument('--log-level', default='INFO', choices=['DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL'], 
                        help='Logging level')
    
    args = parser.parse_args()
    
    # Set log level
    logging.getLogger().setLevel(getattr(logging, args.log_level))
    logger.setLevel(getattr(logging, args.log_level))
    
    start_service(args.model, args.host, args.port, args.workers)
