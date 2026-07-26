import os
import re

directories = ['quickstart', 'demo-multi', 'config', 'config/samples', 'test', 'README.md', 'PROJECT', 'AGENTS.md']

def replace_in_file(filepath):
    try:
        with open(filepath, 'r') as f:
            content = f.read()
    except Exception as e:
        print(f"Error reading {filepath}: {e}")
        return

    # Replace XAccessPolicy with AccessPolicy
    new_content = content.replace('XAccessPolicy', 'AccessPolicy')
    new_content = new_content.replace('xaccesspolicy', 'accesspolicy')
    new_content = new_content.replace('xaccesspolicies', 'accesspolicies')
    new_content = new_content.replace('XAccessPolicies', 'AccessPolicies')
    
    if new_content != content:
        with open(filepath, 'w') as f:
            f.write(new_content)
        print(f"Updated {filepath}")

for path in directories:
    if os.path.isfile(path):
        replace_in_file(path)
    elif os.path.isdir(path):
        for root, _, files in os.walk(path):
            for file in files:
                if file.endswith(('.yaml', '.yml', '.sh', '.md', '.go')):
                    replace_in_file(os.path.join(root, file))
