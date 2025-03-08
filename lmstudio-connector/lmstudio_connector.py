# lmstudio_connector.py
# Service to connect to a local LMStudio server running a language model

from flask import Flask, request, jsonify
import requests
import logging
import argparse
import time
import json

app = Flask(__name__)

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger('lmstudio_connector')

# LMStudio API configuration
lmstudio_url = None

@app.route('/completion', methods=['POST'])
def completion():
    """Handle completion requests and forward to LMStudio."""
    if request.method == 'POST':
        try:
            # Parse JSON request
            data = request.get_json()
            
            if not data or 'prompt' not in data:
                return jsonify({'error': 'Missing prompt in request'}), 400
            
            prompt = data['prompt']
            max_tokens = data.get('max_tokens', 1000)
            temperature = data.get('temperature', 0.2)
            
            # Format the request for LMStudio
            lmstudio_request = {
                "messages": [
                    {"role": "user", "content": prompt}
                ],
                "temperature": temperature,
                "max_tokens": max_tokens,
                "stream": False
            }
            
            # Log request summary
            prompt_preview = prompt[:100] + "..." if len(prompt) > 100 else prompt
            logger.info(f"Sending request to LMStudio: prompt='{prompt_preview}', max_tokens={max_tokens}")
            
            # Send request to LMStudio
            start_time = time.time()
            response = requests.post(
                f"{lmstudio_url}/v1/chat/completions",
                json=lmstudio_request,
                headers={"Content-Type": "application/json"}
            )
            elapsed_time = time.time() - start_time
            
            if response.status_code != 200:
                logger.error(f"LMStudio API error: {response.status_code} - {response.text}")
                return jsonify({'error': f"LMStudio API error: {response.text}"}), 500
            
            # Parse response
            lmstudio_response = response.json()
            
            if 'choices' not in lmstudio_response or len(lmstudio_response['choices']) == 0:
                logger.error(f"Unexpected response format from LMStudio: {lmstudio_response}")
                return jsonify({'error': 'Unexpected response format from LMStudio'}), 500
            
            content = lmstudio_response['choices'][0]['message']['content']
            usage = lmstudio_response.get('usage', {})
            total_tokens = usage.get('total_tokens', 0)
            
            logger.info(f"Received response from LMStudio ({elapsed_time:.2f}s, {total_tokens} tokens)")
            
            return jsonify({
                'text': content,
                'tokens_used': total_tokens
            })
            
        except Exception as e:
            logger.error(f"Error processing request: {e}")
            return jsonify({'error': str(e)}), 500

def start_service(lmstudio_endpoint, host, port):
    """Start the LMStudio connector service."""
    global lmstudio_url
    
    lmstudio_url = lmstudio_endpoint
    
    # Check if LMStudio is reachable
    try:
        response = requests.get(f"{lmstudio_url}/v1/models")
        if response.status_code == 200:
            models = response.json()
            logger.info(f"Connected to LMStudio. Available models: {models}")
        else:
            logger.warning(f"LMStudio API responded with status {response.status_code}")
    except Exception as e:
        logger.warning(f"Could not connect to LMStudio at {lmstudio_url}: {e}")
        logger.warning("Service will start anyway, but verify LMStudio is running")
    
    logger.info(f"Starting server on {host}:{port}")
    app.run(host=host, port=port)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='Start the LMStudio connector service')
    parser.add_argument('--lmstudio-url', default='http://localhost:1234', help='LMStudio API URL')
    parser.add_argument('--host', default='0.0.0.0', help='Host to bind the server to')
    parser.add_argument('--port', type=int, default=8081, help='Port to bind the server to')
    
    args = parser.parse_args()
    
    start_service(args.lmstudio_url, args.host, args.port)
